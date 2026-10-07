package store

import (
	"context"
	"strings"
	"time"
)

// PendingReceipts devuelve hasta limit IDs de mensajes de peerID que ya leímos
// y todavía no le avisamos.
func (s *Store) PendingReceipts(ctx context.Context, peerID string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM messages WHERE peer_id = ? AND receipt = 1 ORDER BY at, rowid LIMIT ?`, peerID, limit)
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

// PeersWithPendingReceipts devuelve los contactos a los que debemos avisos de lectura.
func (s *Store) PeersWithPendingReceipts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT peer_id FROM messages WHERE receipt = 1`)
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

// ClearReceipts marca como enviados los avisos de lectura de ids.
func (s *Store) ClearReceipts(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET receipt = 0 WHERE id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, anySlice(ids)...)
	return err
}

// MarkReadByPeer anota que peerID leyó los mensajes ids que le enviamos y
// devuelve los que cambiaron. Solo afecta mensajes salientes hacia peerID.
func (s *Store) MarkReadByPeer(ctx context.Context, peerID string, ids []string, at time.Time) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	in := `(?` + strings.Repeat(", ?", len(ids)-1) + `)`
	args := append([]any{at.UnixMilli(), peerID}, anySlice(ids)...)
	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET read_at = ?
		WHERE peer_id = ? AND outgoing = 1 AND read_at = 0 AND id IN `+in, args...); err != nil {
		return nil, err
	}
	return s.queryMessages(ctx, `SELECT `+messageSelect+` FROM messages
		WHERE peer_id = ? AND outgoing = 1 AND read_at = ? AND id IN `+in, append([]any{peerID, at.UnixMilli()}, anySlice(ids)...)...)
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
