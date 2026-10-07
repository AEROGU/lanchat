// Package store guarda contactos e historial de mensajes en SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
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
	{
		// unread = 1: mensaje entrante que el usuario aún no ha visto.
		`ALTER TABLE messages ADD COLUMN unread INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX messages_unread ON messages(peer_id) WHERE unread = 1`,
	},
	{
		// kind distingue los mensajes de texto de las ofertas de archivos.
		`ALTER TABLE messages ADD COLUMN kind INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE transfers (
			id         TEXT PRIMARY KEY, -- igual al id del mensaje de la oferta
			peer_id    TEXT NOT NULL,
			outgoing   INTEGER NOT NULL,
			state      INTEGER NOT NULL,
			token      TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			dir        TEXT NOT NULL DEFAULT '',
			error      TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX transfers_state ON transfers(outgoing, state)`,
		`CREATE TABLE transfer_files (
			transfer_id TEXT NOT NULL,
			idx         INTEGER NOT NULL,
			name        TEXT NOT NULL,
			size        INTEGER NOT NULL,
			mod_time    INTEGER NOT NULL DEFAULT 0,
			path        TEXT NOT NULL DEFAULT '',
			done        INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (transfer_id, idx)
		)`,
	},
	{
		// broadcast = 1: se envió a varios contactos a la vez.
		`ALTER TABLE messages ADD COLUMN broadcast INTEGER NOT NULL DEFAULT 0`,
	},
	{
		// read_at: hora (ms) en que el destinatario leyó un mensaje saliente.
		// receipt = 1: hay que avisar al remitente que leímos este entrante.
		`ALTER TABLE messages ADD COLUMN read_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE messages ADD COLUMN receipt INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX messages_receipt ON messages(peer_id) WHERE receipt = 1`,
	},
}

// busyTimeout: espera máxima de una escritura si la base está ocupada.
const busyTimeout = 5 * time.Second

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	// WAL: las lecturas no bloquean a las escrituras.
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)",
		path, busyTimeout.Milliseconds()))
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

const peerColumns = `id, name, hostname, ip, alias, last_seen`

// scanner lo cumplen *sql.Row y *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanPeer(sc scanner) (Peer, error) {
	var p Peer
	var seen int64
	err := sc.Scan(&p.ID, &p.Name, &p.Hostname, &p.IP, &p.Alias, &seen)
	p.LastSeen = time.UnixMilli(seen)
	return p, err
}

func (s *Store) Peer(ctx context.Context, id string) (Peer, bool, error) {
	p, err := scanPeer(s.db.QueryRowContext(ctx, `SELECT `+peerColumns+` FROM peers WHERE id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Peer{}, false, nil
	case err != nil:
		return Peer{}, false, err
	}
	return p, true, nil
}

func (s *Store) Peers(ctx context.Context) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+peerColumns+` FROM peers ORDER BY hostname, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
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
	// Unread: entrante que el usuario aún no ha visto.
	Unread bool
	Kind   Kind
	// Broadcast: se envió a varios contactos a la vez ("Mensaje a varios").
	Broadcast bool
	// ReadAt: cuándo el destinatario leyó este mensaje saliente (cero si no se sabe).
	ReadAt time.Time
}

type Kind int

const (
	KindText Kind = 0
	// KindFiles: oferta de archivos; los detalles están en Transfer con el mismo ID.
	KindFiles Kind = 1
)

// messageColumns son las que se escriben al insertar; messageSelect agrega las
// que solo cambian después.
const messageColumns = `id, peer_id, outgoing, body, at, sent_at, status, unread, kind, broadcast`

const messageSelect = messageColumns + `, read_at`

// InsertMessage guarda el mensaje; inserted es false si ese ID ya existía
// (un reenvío del mismo mensaje).
func (s *Store) InsertMessage(ctx context.Context, m Message) (inserted bool, err error) {
	return s.InsertMessageWithTransfer(ctx, m, nil)
}

// InsertMessageWithTransfer guarda el mensaje y, si t no es nil, su oferta de
// archivos, todo en una transacción. Si el mensaje ya existía no cambia nada.
func (s *Store) InsertMessageWithTransfer(ctx context.Context, m Message, t *Transfer) (inserted bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO messages (`+messageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.PeerID, m.Outgoing, m.Body, m.At.UnixMilli(), m.SentAt.UnixMilli(), m.Status, m.Unread, m.Kind, m.Broadcast)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if t != nil {
		if err := insertTransfer(ctx, tx, *t); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *Store) MarkDelivered(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, StatusDelivered, id)
	return err
}

// MarkRead marca como leídos los mensajes entrantes de peerID; changed indica
// si había alguno sin leer. Con receipts, quedan pendientes de avisar al remitente.
func (s *Store) MarkRead(ctx context.Context, peerID string, receipts bool) (changed bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE messages SET unread = 0, receipt = ? WHERE peer_id = ? AND unread = 1`, receipts, peerID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UnreadCounts devuelve cuántos mensajes sin leer hay por contacto.
func (s *Store) UnreadCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT peer_id, COUNT(*) FROM messages WHERE unread = 1 GROUP BY peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// Pending devuelve los mensajes salientes sin entregar a peerID, del más antiguo al más nuevo.
func (s *Store) Pending(ctx context.Context, peerID string) ([]Message, error) {
	return s.queryMessages(ctx, `SELECT `+messageSelect+` FROM messages
		WHERE peer_id = ? AND status = ? ORDER BY at, rowid`, peerID, StatusPending)
}

// PeersWithPending devuelve los contactos que tienen mensajes por entregar.
func (s *Store) PeersWithPending(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT peer_id FROM messages WHERE status = ?`, StatusPending)
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

// History devuelve hasta limit mensajes con peerID, en orden cronológico,
// anteriores al mensaje beforeID ("" = los más recientes). Para paginar hacia
// atrás se pasa el ID del mensaje más antiguo ya mostrado.
func (s *Store) History(ctx context.Context, peerID, beforeID string, limit int) ([]Message, error) {
	// Cursor (at, rowid): con solo "at" se perderían mensajes del mismo milisegundo.
	at, rowid := int64(math.MaxInt64), int64(math.MaxInt64)
	if beforeID != "" {
		err := s.db.QueryRowContext(ctx,
			`SELECT at, rowid FROM messages WHERE id = ? AND peer_id = ?`, beforeID, peerID).Scan(&at, &rowid)
		if err != nil {
			return nil, fmt.Errorf("mensaje de referencia %s: %w", beforeID, err)
		}
	}
	msgs, err := s.queryMessages(ctx, `SELECT `+messageSelect+` FROM messages
		WHERE peer_id = ? AND (at, rowid) < (?, ?)
		ORDER BY at DESC, rowid DESC LIMIT ?`, peerID, at, rowid, limit)
	if err != nil {
		return nil, err
	}
	slices.Reverse(msgs)
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
		var at, sent, readAt int64
		if err := rows.Scan(&m.ID, &m.PeerID, &m.Outgoing, &m.Body, &at, &sent, &m.Status, &m.Unread,
			&m.Kind, &m.Broadcast, &readAt); err != nil {
			return nil, err
		}
		m.At = time.UnixMilli(at)
		m.SentAt = time.UnixMilli(sent)
		if readAt > 0 {
			m.ReadAt = time.UnixMilli(readAt)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
