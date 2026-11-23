package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/AEROGU/lanchat/internal/i18n"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/transfer"
)

// Editar, responder y reenviar mensajes, como en WhatsApp. Un reenvío es un
// mensaje nuevo, marcado como reenviado: el texto se envía otra vez y un envío
// de archivos se vuelve a ofrecer con los mismos archivos, si siguen en este
// equipo.

const (
	// maxForwardMessages: mensajes que se reenvían de una vez.
	maxForwardMessages = 100
	// maxForwardTargets: contactos y salas a los que se reenvía de una vez.
	maxForwardTargets = 256
)

// EditMessage cambia el texto de un mensaje propio dentro del plazo
// protocol.DeleteWindow (el mismo que para eliminarlo para todos).
func (a *App) EditMessage(ctx context.Context, msgID, body string) (store.Message, error) {
	return a.chat.Edit(ctx, msgID, body)
}

// Reply responde al mensaje msgID (texto o envío de archivos) en su misma
// conversación, contacto o sala: el mensaje nuevo lleva la cita del original,
// como "Responder" en WhatsApp.
func (a *App) Reply(ctx context.Context, msgID, body string) (store.Message, error) {
	orig, ok, err := a.store.Message(ctx, msgID)
	switch {
	case err != nil:
		return store.Message{}, err
	case !ok || orig.Deleted:
		return store.Message{}, i18n.E("app.replyGone")
	case orig.Kind == store.KindRoomEvent:
		return store.Message{}, i18n.E("app.replyEvent")
	}
	author := orig.PeerID
	if orig.Outgoing {
		author = a.Self().ID
	}
	r := store.Reply{ID: orig.ID, Author: author, Text: protocol.ReplySnippet(orig.Body)}
	if orig.RoomID != "" {
		return a.chat.ReplyRoom(ctx, orig.RoomID, body, r)
	}
	if err := a.requireContact(ctx, orig.PeerID); err != nil {
		return store.Message{}, err
	}
	return a.chat.SendReply(ctx, orig.PeerID, body, r)
}

// forwardItem es un mensaje por reenviar: su texto o sus archivos.
type forwardItem struct {
	body  string
	files []transfer.Item // nil en un mensaje de texto
}

// Forward reenvía los mensajes msgIDs, en el orden en que se escribieron, a
// los contactos peers y a las salas rooms. Antes de enviar nada comprueba que
// todo se pueda reenviar: un archivo recibido tiene que seguir en la carpeta
// de descargas, y uno enviado, donde estaba. A las salas no se envían
// archivos. Devuelve los mensajes enviados y un error por cada destino que
// falló (a ese destino no se le envía el resto).
func (a *App) Forward(ctx context.Context, msgIDs, peers, rooms []string) ([]store.Message, error) {
	msgIDs, peers, rooms = unique(msgIDs), unique(peers), unique(rooms)
	switch {
	case len(msgIDs) == 0:
		return nil, i18n.E("app.pickMessage")
	case len(msgIDs) > maxForwardMessages:
		return nil, i18n.E("app.maxForwardMessages", "max", maxForwardMessages)
	case len(peers)+len(rooms) == 0:
		return nil, i18n.E("app.pickTargets")
	case len(peers)+len(rooms) > maxForwardTargets:
		return nil, i18n.E("app.maxForwardTargets", "max", maxForwardTargets)
	}
	items, err := a.forwardItems(ctx, msgIDs)
	if err != nil {
		return nil, err
	}
	if len(rooms) > 0 && slices.ContainsFunc(items, func(it forwardItem) bool { return it.files != nil }) {
		return nil, i18n.E("app.noFilesToRooms")
	}
	for _, p := range peers {
		if err := a.requireContact(ctx, p); err != nil {
			return nil, err
		}
	}
	for _, r := range rooms {
		if _, err := a.activeRoom(ctx, r); err != nil {
			return nil, err
		}
	}

	var out []store.Message
	var errs []error
	for _, p := range peers {
		for _, it := range items {
			var m store.Message
			var err error
			if it.files != nil {
				m, err = a.transfer.Forward(ctx, p, it.files)
			} else {
				m, err = a.chat.SendForwarded(ctx, p, it.body)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", a.contactName(ctx, p), err))
				break
			}
			out = append(out, m)
		}
	}
	for _, r := range rooms {
		for _, it := range items {
			m, err := a.chat.ForwardRoom(ctx, r, it.body)
			if err != nil {
				errs = append(errs, i18n.E("app.roomError", "room", a.roomName(ctx, r), "err", err))
				break
			}
			out = append(out, m)
		}
	}
	return out, errors.Join(errs...)
}

// forwardItems lee los mensajes a reenviar, en el orden en que se escribieron.
func (a *App) forwardItems(ctx context.Context, ids []string) ([]forwardItem, error) {
	msgs := make([]store.Message, 0, len(ids))
	for _, id := range ids {
		m, ok, err := a.store.Message(ctx, id)
		switch {
		case err != nil:
			return nil, err
		case !ok:
			return nil, i18n.E("app.forwardGone")
		case m.Deleted:
			return nil, i18n.E("app.forwardDeleted")
		case m.Kind == store.KindRoomEvent:
			return nil, i18n.E("app.forwardEvent")
		}
		msgs = append(msgs, m)
	}
	slices.SortStableFunc(msgs, func(x, y store.Message) int { return x.At.Compare(y.At) })
	items := make([]forwardItem, len(msgs))
	for i, m := range msgs {
		if m.Kind != store.KindFiles {
			items[i].body = m.Body
			continue
		}
		files, err := a.forwardFiles(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		items[i].files = files
	}
	return items, nil
}

// forwardFiles devuelve los archivos de la oferta id para ofrecerlos otra
// vez, o por qué no se puede: un recibido tiene que haberse descargado y
// seguir en la carpeta de descargas; uno enviado, seguir donde estaba.
func (a *App) forwardFiles(ctx context.Context, id string) ([]transfer.Item, error) {
	t, ok, err := a.store.Transfer(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, i18n.E("app.forwardNoTransfer")
	}
	items := make([]transfer.Item, len(t.Files))
	for i, f := range t.Files {
		switch {
		case !t.Outgoing && !f.Done:
			return nil, i18n.E("app.forwardNotDownloaded", "name", f.Name)
		case !isFile(f.Path) && t.Outgoing:
			return nil, i18n.E("app.forwardMoved", "name", f.Name)
		case !isFile(f.Path):
			return nil, i18n.E("app.forwardMissing", "name", f.Name)
		}
		items[i] = transfer.Item{Path: f.Path, Dir: f.Dir, Name: f.Name}
	}
	return items, nil
}

// isFile dice si path es un archivo que existe.
func isFile(path string) bool {
	fi, err := os.Stat(path)
	return path != "" && err == nil && fi.Mode().IsRegular()
}

// unique quita los repetidos y conserva el orden.
func unique(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// roomName es el nombre de la sala, o su ID si no se encuentra.
func (a *App) roomName(ctx context.Context, id string) string {
	if r, ok, err := a.store.Room(ctx, id); err == nil && ok {
		return i18n.T("fmt.quoted", "text", r.Name)
	}
	return id
}
