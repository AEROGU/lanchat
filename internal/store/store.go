// Package store guarda contactos e historial de mensajes en SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// migrations se aplican en orden; PRAGMA user_version guarda cuántas van.
// Nunca modificar una ya publicada: agregar una nueva al final.
var migrations = [][]string{
	{
		`CREATE TABLE peers (
			id        TEXT PRIMARY KEY,
			name      TEXT NOT NULL DEFAULT '',
			hostname  TEXT NOT NULL DEFAULT '',
			ip        TEXT NOT NULL DEFAULT '',
			alias     TEXT NOT NULL DEFAULT '',
			last_seen INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE messages (
			id       TEXT PRIMARY KEY,
			peer_id  TEXT NOT NULL,
			outgoing INTEGER NOT NULL,
			body     TEXT NOT NULL,
			at       INTEGER NOT NULL,
			sent_at  INTEGER NOT NULL,
			status   INTEGER NOT NULL
		)`,
		`CREATE INDEX messages_peer_at ON messages(peer_id, at)`,
		`CREATE INDEX messages_pending ON messages(peer_id, at) WHERE status = 0`,
	},
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// Una sola conexión: evita bloqueos entre escrituras concurrentes.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrando %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range migrations[v] {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Peer es un contacto recordado, aunque ahora no esté conectado.
type Peer struct {
	ID       string
	Name     string
	Hostname string
	IP       string
	// Alias es el nombre que le puso el usuario local.
	Alias    string
	LastSeen time.Time
}

// UpsertPeer guarda los datos anunciados por un equipo sin tocar su alias.
func (s *Store) UpsertPeer(ctx context.Context, p Peer) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO peers (id, name, hostname, ip, last_seen) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, hostname = excluded.hostname,
			ip = excluded.ip, last_seen = excluded.last_seen`,
		p.ID, p.Name, p.Hostname, p.IP, p.LastSeen.UnixMilli())
	return err
}

func (s *Store) SetAlias(ctx context.Context, id, alias string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE peers SET alias = ? WHERE id = ?`, alias, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("contacto %s desconocido", id)
	}
	return nil
}

func (s *Store) Peer(ctx context.Context, id string) (Peer, bool, error) {
	var p Peer
	var seen int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, hostname, ip, alias, last_seen FROM peers WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Hostname, &p.IP, &p.Alias, &seen)
	if err == sql.ErrNoRows {
		return p, false, nil
	}
	p.LastSeen = time.UnixMilli(seen)
	return p, err == nil, err
}

func (s *Store) Peers(ctx context.Context) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hostname, ip, alias, last_seen FROM peers ORDER BY hostname, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var seen int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Hostname, &p.IP, &p.Alias, &seen); err != nil {
			return nil, err
		}
		p.LastSeen = time.UnixMilli(seen)
		out = append(out, p)
	}
	return out, rows.Err()
}

type Status int

const (
	// StatusPending: mensaje saliente aún no confirmado por el destinatario.
	StatusPending Status = 0
	// StatusDelivered: el destinatario lo guardó (siempre así en los entrantes).
	StatusDelivered Status = 1
)

type Message struct {
	ID       string
	PeerID   string
	Outgoing bool
	Body     string
	// At es la hora local (escritura o recepción); ordena la conversación.
	At time.Time
	// SentAt es la hora del remitente.
	SentAt time.Time
	Status Status
}

// InsertMessage guarda el mensaje; inserted es false si ese ID ya existía
// (un reenvío del mismo mensaje).
func (s *Store) InsertMessage(ctx context.Context, m Message) (inserted bool, err error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO messages (id, peer_id, outgoing, body, at, sent_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.PeerID, m.Outgoing, m.Body, m.At.UnixMilli(), m.SentAt.UnixMilli(), m.Status)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) MarkDelivered(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, StatusDelivered, id)
	return err
}

// Pending devuelve los mensajes salientes sin entregar a peerID, del más antiguo al más nuevo.
func (s *Store) Pending(ctx context.Context, peerID string) ([]Message, error) {
	return s.queryMessages(ctx, `
		SELECT id, peer_id, outgoing, body, at, sent_at, status FROM messages
		WHERE peer_id = ? AND status = 0 ORDER BY at, rowid`, peerID)
}

// PeersWithPending devuelve los contactos que tienen mensajes por entregar.
func (s *Store) PeersWithPending(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT peer_id FROM messages WHERE status = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// History devuelve hasta limit mensajes con peerID anteriores a before (cero =
// los más recientes), en orden cronológico.
func (s *Store) History(ctx context.Context, peerID string, before time.Time, limit int) ([]Message, error) {
	b := int64(1<<63 - 1)
	if !before.IsZero() {
		b = before.UnixMilli()
	}
	msgs, err := s.queryMessages(ctx, `
		SELECT id, peer_id, outgoing, body, at, sent_at, status FROM messages
		WHERE peer_id = ? AND at < ? ORDER BY at DESC, rowid DESC LIMIT ?`, peerID, b, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *Store) queryMessages(ctx context.Context, q string, args ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var at, sent int64
		if err := rows.Scan(&m.ID, &m.PeerID, &m.Outgoing, &m.Body, &at, &sent, &m.Status); err != nil {
			return nil, err
		}
		m.At = time.UnixMilli(at)
		m.SentAt = time.UnixMilli(sent)
		out = append(out, m)
	}
	return out, rows.Err()
}
