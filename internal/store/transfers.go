package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type TransferState int

const (
	// TransferOffered: esperando que el destinatario acepte.
	TransferOffered TransferState = iota
	// TransferDownloading: aceptada; el destinatario está descargando.
	TransferDownloading
	// TransferCompleted: todos los archivos llegaron y se verificaron.
	TransferCompleted
	TransferRejected
	TransferCanceled
	TransferExpired
	// TransferFailed: la descarga falló; el destinatario puede reintentar.
	TransferFailed
)

var transferStateNames = [...]string{"offered", "downloading", "completed", "rejected", "canceled", "expired", "failed"}

func (s TransferState) String() string {
	if int(s) < len(transferStateNames) {
		return transferStateNames[s]
	}
	return "unknown"
}

// Final indica que la transferencia ya no puede cambiar.
func (s TransferState) Final() bool {
	switch s {
	case TransferCompleted, TransferRejected, TransferCanceled, TransferExpired:
		return true
	}
	return false
}

type TransferFile struct {
	Index   int
	Name    string
	Size    int64
	ModTime time.Time // saliente: para detectar si el archivo cambió
	// Dir es la subcarpeta relativa con "/" ("Proyecto/planos"; "" = suelto).
	// En una entrante, tras aceptar, es la carpeta local (puede ser "Proyecto (1)").
	Dir string
	// Path: saliente, el archivo a enviar; entrante, dónde quedó guardado.
	Path string
	Done bool
	// Thumb: miniatura JPEG (vista previa) al guardar la transferencia. Al
	// leerla solo se indica HasThumb; los bytes, con Thumbs.
	Thumb    []byte
	HasThumb bool
}

// Transfer es una oferta de archivos, enviada o recibida. Su ID es el del
// mensaje del chat que la transporta.
type Transfer struct {
	ID       string
	PeerID   string
	Outgoing bool
	State    TransferState
	// Token autoriza la descarga; lo genera el remitente.
	Token     string
	ExpiresAt time.Time
	// Dir: entrante, la carpeta donde se guardan los archivos.
	Dir       string
	Error     string
	UpdatedAt time.Time
	Files     []TransferFile
}

func (t Transfer) TotalSize() int64 {
	var n int64
	for _, f := range t.Files {
		n += f.Size
	}
	return n
}

func insertTransfer(ctx context.Context, tx *sql.Tx, t Transfer) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transfers (id, peer_id, outgoing, state, token, expires_at, dir, error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.PeerID, t.Outgoing, t.State, t.Token, t.ExpiresAt.UnixMilli(), t.Dir, t.Error,
		time.Now().UnixMilli()); err != nil {
		return err
	}
	for _, f := range t.Files {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transfer_files (transfer_id, idx, name, size, mod_time, path, done, dir, thumb)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, f.Index, f.Name, f.Size, f.ModTime.UnixNano(), f.Path, f.Done, f.Dir, nullBytes(f.Thumb)); err != nil {
			return err
		}
	}
	return nil
}

const transferColumns = `id, peer_id, outgoing, state, token, expires_at, dir, error, updated_at`

// Transfer devuelve una transferencia con sus archivos.
func (s *Store) Transfer(ctx context.Context, id string) (Transfer, bool, error) {
	ts, err := s.queryTransfers(ctx, `SELECT `+transferColumns+` FROM transfers WHERE id = ?`, id)
	if err != nil || len(ts) == 0 {
		return Transfer{}, false, err
	}
	return ts[0], true, nil
}

// TransfersByID devuelve las transferencias de los IDs indicados (los que no
// existen se omiten).
func (s *Store) TransfersByID(ctx context.Context, ids []string) (map[string]Transfer, error) {
	out := make(map[string]Transfer, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ts, err := s.queryTransfers(ctx, `SELECT `+transferColumns+` FROM transfers WHERE id IN (?`+
		strings.Repeat(", ?", len(ids)-1)+`)`, anySlice(ids)...)
	for _, t := range ts {
		out[t.ID] = t
	}
	return out, err
}

// TransfersInState devuelve las transferencias en alguno de los estados dados.
func (s *Store) TransfersInState(ctx context.Context, outgoing bool, states ...TransferState) ([]Transfer, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := []any{outgoing}
	for _, st := range states {
		args = append(args, st)
	}
	return s.queryTransfers(ctx, `SELECT `+transferColumns+` FROM transfers
		WHERE outgoing = ? AND state IN (?`+strings.Repeat(", ?", len(states)-1)+`)`, args...)
}

// SetTransferState cambia el estado (y el mensaje de error, vacío si no hay).
func (s *Store) SetTransferState(ctx context.Context, id string, state TransferState, errMsg string) error {
	return s.execOne(ctx, `UPDATE transfers SET state = ?, error = ?, updated_at = ? WHERE id = ?`,
		state, errMsg, time.Now().UnixMilli(), id)
}

func (s *Store) SetTransferDir(ctx context.Context, id, dir string) error {
	return s.execOne(ctx, `UPDATE transfers SET dir = ?, updated_at = ? WHERE id = ?`,
		dir, time.Now().UnixMilli(), id)
}

// MarkFileDone marca un archivo como terminado; path es dónde quedó (entrante)
// o "" para no cambiarlo (saliente).
func (s *Store) MarkFileDone(ctx context.Context, id string, idx int, path string) error {
	return s.execOne(ctx, `UPDATE transfer_files SET done = 1, path = CASE WHEN ? = '' THEN path ELSE ? END
		WHERE transfer_id = ? AND idx = ?`, path, path, id, idx)
}

// SetThumb guarda la miniatura de un archivo (la genera quien lo recibe si el
// remitente no la mandó).
func (s *Store) SetThumb(ctx context.Context, id string, idx int, thumb []byte) error {
	return s.execOne(ctx, `UPDATE transfer_files SET thumb = ? WHERE transfer_id = ? AND idx = ?`,
		nullBytes(thumb), id, idx)
}

// Thumbs devuelve las miniaturas de una transferencia (índice → JPEG).
func (s *Store) Thumbs(ctx context.Context, id string) (map[int][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT idx, thumb FROM transfer_files
		WHERE transfer_id = ? AND thumb IS NOT NULL`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int][]byte{}
	for rows.Next() {
		var idx int
		var b []byte
		if err := rows.Scan(&idx, &b); err != nil {
			return nil, err
		}
		out[idx] = b
	}
	return out, rows.Err()
}

// nullBytes guarda NULL en lugar de un BLOB vacío ("sin miniatura").
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (s *Store) execOne(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrNotFound: no existe el registro a modificar.
var ErrNotFound = errors.New("no encontrado")

func (s *Store) queryTransfers(ctx context.Context, q string, args ...any) ([]Transfer, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []Transfer
	for rows.Next() {
		var t Transfer
		var expires, updated int64
		if err := rows.Scan(&t.ID, &t.PeerID, &t.Outgoing, &t.State, &t.Token, &expires,
			&t.Dir, &t.Error, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		t.ExpiresAt = time.UnixMilli(expires)
		t.UpdatedAt = time.UnixMilli(updated)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Con una sola conexión, los archivos se leen después de cerrar rows.
	for i := range out {
		if out[i].Files, err = s.transferFiles(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) transferFiles(ctx context.Context, id string) ([]TransferFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT idx, name, size, mod_time, path, done, dir, thumb IS NOT NULL
		FROM transfer_files WHERE transfer_id = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransferFile
	for rows.Next() {
		var f TransferFile
		var mod int64
		if err := rows.Scan(&f.Index, &f.Name, &f.Size, &mod, &f.Path, &f.Done, &f.Dir, &f.HasThumb); err != nil {
			return nil, err
		}
		f.ModTime = time.Unix(0, mod)
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetFileDirs cambia las subcarpetas locales de los archivos de una
// transferencia (índice → carpeta), en una transacción.
func (s *Store) SetFileDirs(ctx context.Context, id string, dirs map[int]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for idx, dir := range dirs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE transfer_files SET dir = ? WHERE transfer_id = ? AND idx = ?`, dir, id, idx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
