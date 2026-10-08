package app

import (
	"context"
	"testing"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/testutil"
)

func roomEvent(match func(store.Room) bool) func(any) bool {
	return func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.RoomChanged && match(e.Room)
	}
}

func TestRooms(t *testing.T) {
	ctx := context.Background()
	dirB := testutil.TempDir(t)
	a := startNode(t, testutil.TempDir(t), "PC-A")
	b := startNode(t, dirB, "PC-B", a.udpPort())
	c := startNode(t, testutil.TempDir(t), "PC-C", a.udpPort(), b.udpPort())
	idA, idB, idC := a.app.Self().ID, b.app.Self().ID, c.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))
	waitFor(t, a, "A ve a C", peerEvent(discovery.PeerOnline, idC))
	waitFor(t, b, "B ve a C", peerEvent(discovery.PeerOnline, idC))

	if _, err := a.app.CreateRoom(ctx, "  ", []string{idB}); err == nil {
		t.Error("sala sin nombre")
	}
	if _, err := a.app.CreateRoom(ctx, "Proyecto", []string{"desconocido"}); err == nil {
		t.Error("sala con un desconocido")
	}
	room, err := a.app.CreateRoom(ctx, "Proyecto", []string{idB, idB, idA})
	if err != nil {
		t.Fatal(err)
	}
	if len(room.Members) != 2 || room.Version != 1 {
		t.Fatalf("sala creada: %+v", room)
	}
	waitFor(t, b, "B conoce la sala", roomEvent(func(r store.Room) bool { return r.ID == room.ID && r.Name == "Proyecto" }))
	ev := waitFor(t, b, "B recibe el aviso", chatEvent(chat.MessageReceived, "PC-A creó la sala «Proyecto»")).(chat.Event)
	if ev.Message.Kind != store.KindRoomEvent || ev.Message.Unread {
		t.Errorf("el aviso debía ser un evento ya leído: %+v", ev.Message)
	}

	// Mensaje de B: le llega a A con B como autor.
	if _, err := b.app.SendRoom(ctx, room.ID, "hola sala"); err != nil {
		t.Fatal(err)
	}
	ev = waitFor(t, a, "A recibe", chatEvent(chat.MessageReceived, "hola sala")).(chat.Event)
	if ev.Message.RoomID != room.ID || ev.Message.PeerID != idB {
		t.Errorf("mensaje de sala mal registrado: %+v", ev.Message)
	}
	waitFor(t, b, "B confirma la entrega", chatEvent(chat.MessageDelivered, "hola sala"))
	if rooms, _ := a.app.Rooms(ctx); len(rooms) != 1 || rooms[0].Unread != 1 {
		t.Errorf("salas de A: %+v", rooms)
	}
	if n, _ := a.app.TotalUnread(ctx); n != 1 {
		t.Errorf("total sin leer = %d", n)
	}
	if h, _ := a.app.History(ctx, idB, "", 10); len(h) != 0 {
		t.Errorf("los mensajes de sala no van en la conversación 1 a 1: %+v", h)
	}

	// B agrega a C, que solo ve lo que pasa desde que entra.
	if err := b.app.AddRoomMembers(ctx, room.ID, []string{idC}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "C recibe el aviso", chatEvent(chat.MessageReceived, "PC-B agregó a PC-C"))
	waitFor(t, a, "A ve a C en la sala", roomEvent(func(r store.Room) bool { return len(r.Members) == 3 }))
	if _, err := c.app.SendRoom(ctx, room.ID, "soy nuevo"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A recibe a C", chatEvent(chat.MessageReceived, "soy nuevo"))
	waitFor(t, b, "B recibe a C", chatEvent(chat.MessageReceived, "soy nuevo"))
	if h, _ := c.app.RoomHistory(ctx, room.ID, "", 10); len(h) != 2 {
		t.Errorf("C debía ver solo desde que entró: %+v", h)
	}

	if err := c.app.RenameRoom(ctx, room.ID, "Proyecto 2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A ve el nombre nuevo", roomEvent(func(r store.Room) bool { return r.Name == "Proyecto 2" }))

	// C sale: los demás lo saben y ya no le llega nada.
	if err := c.app.LeaveRoom(ctx, room.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B sabe que C salió", chatEvent(chat.MessageReceived, "PC-C salió de la sala"))
	if r, _, _ := b.app.Room(ctx, room.ID); len(r.Members) != 2 || r.Name != "Proyecto 2" {
		t.Errorf("sala en B tras la salida: %+v", r.Room)
	}
	if _, err := c.app.SendRoom(ctx, room.ID, "sigo aquí"); err == nil {
		t.Error("C ya no debía poder escribir")
	}

	// B cerrado: el mensaje espera hasta que vuelve.
	b.stop()
	waitFor(t, a, "A ve salir a B", peerEvent(discovery.PeerOffline, idB))
	m, err := a.app.SendRoom(ctx, room.ID, "para cuando vuelvas")
	if err != nil || m.Status != store.StatusPending {
		t.Fatalf("debía quedar pendiente: %+v, %v", m, err)
	}
	b2 := startNode(t, dirB, "PC-B", a.udpPort())
	waitFor(t, b2, "B recibe el pendiente", chatEvent(chat.MessageReceived, "para cuando vuelvas"))
	waitFor(t, a, "A confirma el pendiente", chatEvent(chat.MessageDelivered, "para cuando vuelvas"))
}
