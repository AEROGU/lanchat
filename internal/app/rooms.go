package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/AEROGU/lanchat/internal/ids"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// Room es una sala con lo que necesita la interfaz.
type Room struct {
	store.Room
	Unread int
}

// Rooms devuelve las salas (también aquellas de las que este equipo salió).
func (a *App) Rooms(ctx context.Context) ([]Room, error) {
	rooms, err := a.store.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	unread, err := a.store.RoomUnreadCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Room, len(rooms))
	for i, r := range rooms {
		out[i] = Room{Room: r, Unread: unread[r.ID]}
	}
	return out, nil
}

// Room busca una sala visible (las borradas no cuentan).
func (a *App) Room(ctx context.Context, id string) (Room, bool, error) {
	r, ok, err := a.store.Room(ctx, id)
	if err != nil || !ok || r.Hidden {
		return Room{}, false, err
	}
	unread, err := a.store.RoomUnreadCounts(ctx)
	return Room{Room: r, Unread: unread[id]}, true, err
}

// CreateRoom crea una sala con este equipo y los contactos indicados.
func (a *App) CreateRoom(ctx context.Context, name string, members []string) (store.Room, error) {
	name, err := roomName(name)
	if err != nil {
		return store.Room{}, err
	}
	added, err := a.newMembers(ctx, nil, members)
	if err != nil {
		return store.Room{}, err
	}
	r := store.Room{ID: ids.New(), Name: name, Members: append([]string{a.Self().ID}, added...), Version: 1}
	if err := a.saveRoom(ctx, r); err != nil {
		return store.Room{}, err
	}
	_, err = a.chat.SendRoom(ctx, r.ID, fmt.Sprintf("%s creó la sala «%s»", a.selfName(), name), true)
	return r, err
}

// AddRoomMembers agrega contactos a la sala (cualquier miembro puede hacerlo).
// Los nuevos ven los mensajes desde que entran.
func (a *App) AddRoomMembers(ctx context.Context, roomID string, members []string) error {
	r, err := a.activeRoom(ctx, roomID)
	if err != nil {
		return err
	}
	added, err := a.newMembers(ctx, r.Members, members)
	if err != nil {
		return err
	}
	r.Members = append(r.Members, added...)
	r.Version++
	if err := a.saveRoom(ctx, r); err != nil {
		return err
	}
	names := make([]string, len(added))
	for i, id := range added {
		names[i] = a.contactName(ctx, id)
	}
	_, err = a.chat.SendRoom(ctx, roomID, fmt.Sprintf("%s agregó a %s", a.selfName(), joinNames(names)), true)
	return err
}

// RenameRoom cambia el nombre de la sala para todos los miembros.
func (a *App) RenameRoom(ctx context.Context, roomID, name string) error {
	name, err := roomName(name)
	if err != nil {
		return err
	}
	r, err := a.activeRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if r.Name == name {
		return nil
	}
	r.Name = name
	r.Version++
	if err := a.saveRoom(ctx, r); err != nil {
		return err
	}
	_, err = a.chat.SendRoom(ctx, roomID, fmt.Sprintf("%s cambió el nombre de la sala a «%s»", a.selfName(), name), true)
	return err
}

// LeaveRoom saca a este equipo de la sala y avisa a los demás. El historial
// se conserva pero ya no llegan mensajes.
func (a *App) LeaveRoom(ctx context.Context, roomID string) error {
	r, err := a.activeRoom(ctx, roomID)
	if err != nil {
		return err
	}
	self := a.Self().ID
	r.Members = slices.DeleteFunc(r.Members, func(id string) bool { return id == self })
	r.Version++
	r.Left = true
	if err := a.saveRoom(ctx, r); err != nil {
		return err
	}
	_, err = a.chat.SendRoom(ctx, roomID, fmt.Sprintf("%s salió de la sala", a.selfName()), true)
	return err
}

// SendRoom envía un mensaje a la sala.
func (a *App) SendRoom(ctx context.Context, roomID, body string) (store.Message, error) {
	return a.chat.SendRoom(ctx, roomID, body, false)
}

func (a *App) RoomHistory(ctx context.Context, roomID, beforeID string, limit int) ([]store.Message, error) {
	return a.store.RoomHistory(ctx, roomID, beforeID, limit)
}

func (a *App) MarkRoomRead(ctx context.Context, roomID string) (bool, error) {
	return a.store.MarkRoomRead(ctx, roomID)
}

func (a *App) activeRoom(ctx context.Context, id string) (store.Room, error) {
	r, ok, err := a.store.Room(ctx, id)
	switch {
	case err != nil:
		return r, err
	case !ok:
		return r, errors.New("sala inexistente")
	case r.Left:
		return r, errors.New("saliste de esta sala")
	}
	return r, nil
}

func (a *App) saveRoom(ctx context.Context, r store.Room) error {
	if err := a.store.SaveRoom(ctx, r); err != nil {
		return err
	}
	a.chat.NotifyRoomChanged(r)
	return nil
}

// newMembers valida los contactos a agregar: conocidos, no repetidos ni ya
// miembros, y sin pasar del máximo.
func (a *App) newMembers(ctx context.Context, current, ids []string) ([]string, error) {
	self := a.Self().ID
	var out []string
	for _, id := range ids {
		if id == self || slices.Contains(current, id) || slices.Contains(out, id) {
			continue
		}
		if err := a.requireContact(ctx, id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("elige al menos un contacto nuevo")
	}
	if total := max(len(current), 1) + len(out); total > protocol.MaxRoomMembers {
		return nil, fmt.Errorf("una sala admite hasta %d miembros", protocol.MaxRoomMembers)
	}
	return out, nil
}

func roomName(name string) (string, error) {
	name, err := cleanName(name)
	if err == nil && name == "" {
		err = errors.New("ponle un nombre a la sala")
	}
	return name, err
}

func (a *App) selfName() string {
	s := a.Self()
	if s.Name != "" {
		return s.Name
	}
	return s.Hostname
}

func (a *App) contactName(ctx context.Context, id string) string {
	if c, ok, err := a.Contact(ctx, id); err == nil && ok {
		return c.DisplayName()
	}
	return id
}

// joinNames: "Ana", "Ana y Luis", "Ana, Luis y Eva".
func joinNames(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " y " + names[len(names)-1]
}
