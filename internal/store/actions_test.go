package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestEffectiveAdmins(t *testing.T) {
	cases := []struct {
		members, admins, want []string
	}{
		{[]string{"a", "b", "c"}, nil, []string{"a"}},                     // sala anterior: el más antiguo
		{[]string{"a", "b", "c"}, []string{"c", "b"}, []string{"b", "c"}}, // en el orden de los miembros
		{[]string{"b", "c"}, []string{"a"}, []string{"b"}},                // el administrador salió
		{[]string{"a", "b"}, []string{"b", "x"}, []string{"b"}},           // se ignora a quien no es miembro
		{nil, []string{"a"}, nil},                                         // sala vacía
	}
	for _, c := range cases {
		r := Room{Members: c.members, Admins: c.admins}
		if got := r.EffectiveAdmins(); !slices.Equal(got, c.want) {
			t.Errorf("EffectiveAdmins(%v, %v) = %v, quería %v", c.members, c.admins, got, c.want)
		}
	}
	if r := (Room{Members: []string{"a", "b"}, Admins: []string{"b"}}); r.IsAdmin("a") || !r.IsAdmin("b") {
		t.Error("IsAdmin")
	}
}

func TestRoomAdminsPersist(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	r := Room{ID: "r1", Name: "Sala", Members: []string{"yo", "a", "b"}, Admins: []string{"yo", "b"}, Version: 1}
	if err := s.SaveRoom(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Room(ctx, "r1")
	if !slices.Equal(got.Admins, []string{"yo", "b"}) {
		t.Errorf("admins guardados = %v", got.Admins)
	}
	r.Admins, r.Version = []string{"a"}, 2
	if ok, err := s.ApplyRoom(ctx, r); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if got, _, _ = s.Room(ctx, "r1"); !slices.Equal(got.Admins, []string{"a"}) {
		t.Errorf("admins aplicados = %v", got.Admins)
	}
}

func TestReactions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.UnixMilli(1_700_000_000_000)
	m := Message{ID: "m1", PeerID: "a", Body: "hola", At: now, SentAt: now, Status: StatusDelivered}
	s.InsertMessage(ctx, m)

	set := func(peer, emoji string, at time.Time, want bool) {
		t.Helper()
		got, err := s.SetReaction(ctx, "m1", peer, emoji, at)
		if err != nil || got != want {
			t.Errorf("SetReaction(%q, %q) = %v, %v; quería %v", peer, emoji, got, err, want)
		}
	}
	set("a", "👍", now.Add(time.Second), true)
	set("", "❤️", now.Add(2*time.Second), true)
	set("a", "👍", now.Add(3*time.Second), false) // la misma: nada cambia
	set("a", "😂", now, false)                    // más vieja que la guardada: se ignora
	set("a", "😮", now.Add(4*time.Second), true)  // la reemplaza
	set("", "", now.Add(time.Second), false)     // quitar con hora vieja: se ignora
	got, ok, err := s.Message(ctx, "m1")
	if err != nil || !ok || len(got.Reactions) != 2 {
		t.Fatalf("Message = %+v %v %v", got, ok, err)
	}
	if got.Reactions[0].PeerID != "" || got.Reactions[0].Emoji != "❤️" || got.Reactions[1].Emoji != "😮" {
		t.Errorf("reacciones = %+v", got.Reactions)
	}
	set("", "", now.Add(5*time.Second), true)
	if h, _ := s.History(ctx, "a", "", 10); len(h) != 1 || len(h[0].Reactions) != 1 {
		t.Errorf("el historial trae las reacciones: %+v", h)
	}
	// Un mensaje sin reacciones las trae vacías, no nil ("no se consultó").
	s.InsertMessage(ctx, Message{ID: "m2", PeerID: "a", Body: "x", At: now, SentAt: now})
	if got, _, _ := s.Message(ctx, "m2"); got.Reactions == nil || len(got.Reactions) != 0 {
		t.Errorf("sin reacciones = %#v", got.Reactions)
	}
}

func TestDeleteMessage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	in := Message{ID: "m1", PeerID: "a", Body: "TEXTO-A-BORRAR", At: now, SentAt: now, Status: StatusDelivered, Unread: true}
	s.InsertMessage(ctx, in)
	s.SetReaction(ctx, "m1", "", "👍", now)

	got, wasUnread, err := s.DeleteMessage(ctx, "m1")
	if err != nil || !wasUnread || !got.Deleted || got.Body != "" || got.Unread || len(got.Reactions) != 0 {
		t.Fatalf("DeleteMessage = %+v %v %v", got, wasUnread, err)
	}
	if n, _ := s.UnreadCounts(ctx); n["a"] != 0 {
		t.Errorf("un mensaje eliminado no cuenta como no leído: %v", n)
	}
	if _, _, err := s.DeleteMessage(ctx, "nada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("mensaje inexistente: %v", err)
	}

	// Un saliente sin entregar ya no se entrega (ni 1 a 1 ni en sala).
	out := Message{ID: "o1", PeerID: "a", Outgoing: true, Body: "x", At: now, SentAt: now, Status: StatusPending}
	s.InsertMessage(ctx, out)
	s.SaveRoom(ctx, Room{ID: "r1", Name: "S", Members: []string{"yo", "a", "b"}, Version: 1})
	room := Message{ID: "o2", RoomID: "r1", Outgoing: true, Body: "y", At: now, SentAt: now, Status: StatusPending}
	s.InsertRoomMessage(ctx, room, []string{"a", "b"})
	s.MarkRoomDelivered(ctx, "o2", "a")
	if to, _ := s.RoomRecipients(ctx, "o2"); len(to) != 2 {
		t.Errorf("destinatarios = %v", to)
	}
	s.DeleteMessage(ctx, "o1")
	s.DeleteMessage(ctx, "o2")
	if p, _ := s.Pending(ctx, "a"); len(p) != 0 {
		t.Errorf("pendientes 1 a 1: %+v", p)
	}
	if p, _ := s.PendingRoomDeliveries(ctx, "b"); len(p) != 0 {
		t.Errorf("pendientes de sala: %+v", p)
	}
	// Quien ya lo recibió sigue en la lista: hay que avisarle que se eliminó.
	if to, _ := s.RoomRecipients(ctx, "o2"); !slices.Equal(to, []string{"a"}) {
		t.Errorf("destinatarios tras eliminar = %v", to)
	}
}

func TestOutbox(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.EnqueueOps(ctx, []string{"a", "b"}, `{"type":"delete","msg":"m1"}`); err != nil {
		t.Fatal(err)
	}
	s.EnqueueOps(ctx, []string{"a"}, `{"type":"react","msg":"m2","emoji":"👍"}`)
	s.EnqueueOps(ctx, nil, `{}`)

	peers, _ := s.PeersWithPending(ctx)
	slices.Sort(peers)
	if !slices.Equal(peers, []string{"a", "b"}) {
		t.Errorf("equipos con pendientes = %v", peers)
	}
	ops, err := s.PendingOps(ctx, "a", 10)
	if err != nil || len(ops) != 2 || ops[0].Payload != `{"type":"delete","msg":"m1"}` {
		t.Fatalf("PendingOps = %+v %v", ops, err)
	}
	if one, _ := s.PendingOps(ctx, "a", 1); len(one) != 1 || one[0].ID != ops[0].ID {
		t.Errorf("el límite respeta el orden: %+v", one)
	}
	if err := s.DeleteOps(ctx, ops[:1]); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.PendingOps(ctx, "a", 10); len(left) != 1 || left[0].ID != ops[1].ID {
		t.Errorf("quedan = %+v", left)
	}
	// Borrar todos los datos conserva solo los avisos de "borrar para todos".
	if err := s.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.PendingOps(ctx, "a", 10); len(a) != 0 {
		t.Errorf("la reacción pendiente debía borrarse: %+v", a)
	}
	if b, _ := s.PendingOps(ctx, "b", 10); len(b) != 1 {
		t.Errorf("el borrado pendiente debía conservarse: %+v", b)
	}
}
