package app

import (
	"context"
	"slices"
	"testing"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/testutil"
)

// trio arranca tres equipos que se ven entre sí.
func trio(t *testing.T) (a, b, c *node) {
	t.Helper()
	a = startNode(t, testutil.TempDir(t), "PC-A")
	b = startNode(t, testutil.TempDir(t), "PC-B", a.udpPort())
	c = startNode(t, testutil.TempDir(t), "PC-C", a.udpPort(), b.udpPort())
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, b.app.Self().ID))
	waitFor(t, a, "A ve a C", peerEvent(discovery.PeerOnline, c.app.Self().ID))
	waitFor(t, b, "B ve a C", peerEvent(discovery.PeerOnline, c.app.Self().ID))
	waitFor(t, c, "C ve a B", peerEvent(discovery.PeerOnline, b.app.Self().ID))
	return a, b, c
}

// msgEvent espera un evento de ese tipo sobre el mensaje id.
func msgEvent(typ chat.EventType, id string) func(any) bool {
	return func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == typ && e.Message.ID == id
	}
}

func TestRoomAdmins(t *testing.T) {
	ctx := context.Background()
	a, b, c := trio(t)
	idA, idB, idC := a.app.Self().ID, b.app.Self().ID, c.app.Self().ID

	room, err := a.app.CreateRoom(ctx, "Proyecto", []string{idB, idC})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(room.EffectiveAdmins(), []string{idA}) {
		t.Fatalf("quien crea la sala la administra: %v", room.EffectiveAdmins())
	}
	inRoom := func(r store.Room) bool { return r.ID == room.ID }
	waitFor(t, b, "B conoce la sala", roomEvent(inRoom))
	waitFor(t, c, "C conoce la sala", roomEvent(inRoom))

	// Quien no administra no puede sacar a nadie ni nombrar administradores.
	if err := b.app.RemoveRoomMember(ctx, room.ID, idC); err == nil {
		t.Error("B no debía poder sacar a C")
	}
	if err := b.app.SetRoomAdmin(ctx, room.ID, idB, true); err == nil {
		t.Error("B no debía poder hacerse administrador")
	}
	if err := a.app.SetRoomAdmin(ctx, room.ID, idA, false); err == nil {
		t.Error("nadie se quita el privilegio a sí mismo")
	}

	// A hace administrador a B.
	if err := a.app.SetRoomAdmin(ctx, room.ID, idB, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B ya administra", roomEvent(func(r store.Room) bool { return r.IsAdmin(idB) && r.IsAdmin(idA) }))
	waitFor(t, c, "C se entera", chatEvent(chat.MessageReceived, "PC-A hizo administrador a PC-B"))

	// B saca a C: C recibe el aviso y queda fuera.
	if err := b.app.RemoveRoomMember(ctx, room.ID, idC); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "C sabe que lo sacaron", chatEvent(chat.MessageReceived, "PC-B sacó a PC-C de la sala"))
	if r, ok, _ := c.app.Room(ctx, room.ID); !ok || !r.Left {
		t.Errorf("C debía quedar fuera de la sala: %+v", r.Room)
	}
	if _, err := c.app.SendRoom(ctx, room.ID, "sigo aquí"); err == nil {
		t.Error("C ya no debía poder escribir")
	}
	waitFor(t, a, "A ve la sala sin C", roomEvent(func(r store.Room) bool { return len(r.Members) == 2 }))

	// B le quita el privilegio a A, que ya no puede sacar a nadie.
	if err := b.app.SetRoomAdmin(ctx, room.ID, idA, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A ya no administra", roomEvent(func(r store.Room) bool { return !r.IsAdmin(idA) }))
	if err := a.app.RemoveRoomMember(ctx, room.ID, idB); err == nil {
		t.Error("A ya no debía poder sacar a B")
	}

	// B, único administrador, sale: administra el miembro más antiguo que queda.
	if err := b.app.LeaveRoom(ctx, room.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A vuelve a administrar", roomEvent(func(r store.Room) bool {
		return len(r.Members) == 1 && r.IsAdmin(idA)
	}))
}

func TestDeleteForEveryone(t *testing.T) {
	ctx := context.Background()
	dirB := testutil.TempDir(t)
	a := startNode(t, testutil.TempDir(t), "PC-A")
	b := startNode(t, dirB, "PC-B", a.udpPort())
	idA, idB := a.app.Self().ID, b.app.Self().ID
	waitFor(t, a, "A ve a B", peerEvent(discovery.PeerOnline, idB))
	waitFor(t, b, "B ve a A", peerEvent(discovery.PeerOnline, idA))

	m, err := a.app.Send(ctx, idB, "esto no era para ti")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe", msgEvent(chat.MessageReceived, m.ID))
	if _, err := b.app.DeleteForEveryone(ctx, m.ID); err == nil {
		t.Error("B no debía poder eliminar un mensaje de A")
	}
	b.app.React(ctx, m.ID, "👍")
	waitFor(t, a, "A ve la reacción", msgEvent(chat.MessageReaction, m.ID))

	del, err := a.app.DeleteForEveryone(ctx, m.ID)
	if err != nil || !del.Deleted || del.Body != "" {
		t.Fatalf("eliminar: %+v, %v", del, err)
	}
	ev := waitFor(t, b, "B lo ve eliminado", msgEvent(chat.MessageDeleted, m.ID)).(chat.Event)
	if !ev.Message.Deleted || ev.Message.Body != "" {
		t.Errorf("evento en B: %+v", ev.Message)
	}
	for _, n := range []*node{a, b} {
		peer := idB
		if n == b {
			peer = idA
		}
		h, _ := n.app.History(ctx, peer, "", 10)
		if len(h) != 1 || !h[0].Deleted || h[0].Body != "" || len(h[0].Reactions) != 0 || h[0].Unread {
			t.Errorf("historial tras eliminar: %+v", h)
		}
	}

	// B desconectado: lo que ya había recibido se le elimina al volver, y lo
	// que no había recibido nunca le llega.
	seen, _ := a.app.Send(ctx, idB, "recibido antes de irse")
	waitFor(t, b, "B recibe", msgEvent(chat.MessageReceived, seen.ID))
	b.stop()
	waitFor(t, a, "A ve salir a B", peerEvent(discovery.PeerOffline, idB))
	unseen, _ := a.app.Send(ctx, idB, "nunca debe llegar")
	for _, id := range []string{seen.ID, unseen.ID} {
		if _, err := a.app.DeleteForEveryone(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	kept, _ := a.app.Send(ctx, idB, "este sí llega")

	b2 := startNode(t, dirB, "PC-B", a.udpPort())
	waitFor(t, b2, "B recibe lo que no se eliminó", msgEvent(chat.MessageReceived, kept.ID))
	waitFor(t, b2, "B elimina lo que ya tenía", msgEvent(chat.MessageDeleted, seen.ID))
	h, _ := b2.app.History(ctx, idA, "", 10)
	for _, msg := range h {
		if msg.ID == unseen.ID || msg.Body == "nunca debe llegar" || msg.Body == "recibido antes de irse" {
			t.Errorf("no debía estar en B: %+v", msg)
		}
	}
}

func TestDeleteAndReactInRoom(t *testing.T) {
	ctx := context.Background()
	a, b, c := trio(t)
	idB, idC := b.app.Self().ID, c.app.Self().ID
	room, err := a.app.CreateRoom(ctx, "Equipo", []string{idB, idC})
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.app.SendRoom(ctx, room.ID, "junta a las 5")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe", msgEvent(chat.MessageReceived, m.ID))
	waitFor(t, c, "C recibe", msgEvent(chat.MessageReceived, m.ID))

	// La reacción de B la ven A y C.
	if _, err := b.app.React(ctx, m.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []*node{a, c} {
		ev := waitFor(t, n, "ve la reacción de B", msgEvent(chat.MessageReaction, m.ID)).(chat.Event)
		if len(ev.Message.Reactions) != 1 || ev.Message.Reactions[0].PeerID != idB || ev.Message.Reactions[0].Emoji != "👍" {
			t.Errorf("reacciones: %+v", ev.Message.Reactions)
		}
	}
	// C también reacciona; B cambia la suya y luego la quita.
	c.app.React(ctx, m.ID, "❤️")
	waitFor(t, a, "A ve la de C", func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.MessageReaction && len(e.Message.Reactions) == 2
	})
	b.app.React(ctx, m.ID, "😂")
	waitFor(t, a, "A ve el cambio de B", func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.MessageReaction && slices.ContainsFunc(e.Message.Reactions,
			func(r store.Reaction) bool { return r.PeerID == idB && r.Emoji == "😂" })
	})
	b.app.React(ctx, m.ID, "")
	waitFor(t, a, "A ve que B la quitó", func(ev any) bool {
		e, ok := ev.(chat.Event)
		return ok && e.Type == chat.MessageReaction && len(e.Message.Reactions) == 1 && e.Message.Reactions[0].PeerID == idC
	})
	if _, err := b.app.React(ctx, m.ID, "hola"); err == nil {
		t.Error("una reacción debe ser un emoji")
	}

	// A lo elimina para todos.
	if _, err := a.app.DeleteForEveryone(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	for _, n := range []*node{b, c} {
		waitFor(t, n, "lo ve eliminado", msgEvent(chat.MessageDeleted, m.ID))
		h, _ := n.app.RoomHistory(ctx, room.ID, "", 10)
		last := h[len(h)-1]
		if !last.Deleted || last.Body != "" || len(last.Reactions) != 0 {
			t.Errorf("mensaje tras eliminar: %+v", last)
		}
	}
}
