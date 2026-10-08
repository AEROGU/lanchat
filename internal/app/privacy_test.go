package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/testutil"
)

func TestDeleteConversationCancelsOffers(t *testing.T) {
	a, b, _ := pair(t)
	ctx := context.Background()
	idA, idB := a.app.Self().ID, b.app.Self().ID
	if _, err := a.app.Send(ctx, idB, "algo privado"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe", chatEvent(chat.MessageReceived, "algo privado"))
	src, _ := writeRandom(t, testutil.TempDir(t), "contrato.pdf", 100)
	m := offer(t, a, b, src)

	// B borra la conversación: la oferta se cancela también para A.
	if err := b.app.DeleteConversation(ctx, idA); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "A ve la oferta cancelada", transferState(m.ID, store.TransferCanceled))
	if h, _ := b.app.History(ctx, idA, "", 10); len(h) != 0 {
		t.Errorf("quedaron mensajes: %+v", h)
	}
	if h, _ := a.app.History(ctx, idB, "", 10); len(h) != 2 {
		t.Errorf("A debía conservar su copia: %d mensajes", len(h))
	}
	if _, ok, _ := b.app.Contact(ctx, idA); !ok {
		t.Error("el contacto debía conservarse")
	}
}

func TestWipeData(t *testing.T) {
	a, b, _ := pair(t)
	ctx := context.Background()
	idA, idB := a.app.Self().ID, b.app.Self().ID
	if err := b.app.SetName("Beto"); err != nil {
		t.Fatal(err)
	}
	b.app.SetStatus("busy", "En junta")
	b.app.SetAlias(ctx, idA, "Jefa")
	if _, err := a.app.Send(ctx, idB, "hola"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe", chatEvent(chat.MessageReceived, "hola"))
	room, err := b.app.CreateRoom(ctx, "Equipo", []string{idA})
	if err != nil {
		t.Fatal(err)
	}

	export := filepath.Join(testutil.TempDir(t), "mis datos.db")
	if err := b.app.ExportData(ctx, export); err != nil {
		t.Fatal(err)
	}
	if err := b.app.WipeData(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.app.TotalUnread(ctx); n != 0 {
		t.Errorf("no leídos = %d", n)
	}
	if h, _ := b.app.History(ctx, idA, "", 10); len(h) != 0 {
		t.Errorf("quedaron mensajes: %+v", h)
	}
	if rooms, _ := b.app.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("quedaron salas: %+v", rooms)
	}
	if s := b.app.Self(); s.Name != "" || s.StatusText != "" {
		t.Errorf("nombre o estado sin borrar: %+v", s)
	}
	c, ok, _ := b.app.Contact(ctx, idA)
	if !ok || c.Alias != "" || c.Fingerprint == "" {
		t.Errorf("contacto tras borrar todo: %+v", c)
	}

	// La copia descargada antes conserva todo.
	cp, err := store.Open(export)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if h, _ := cp.History(ctx, idA, "", 10); len(h) != 1 {
		t.Errorf("historial en la copia: %+v", h)
	}
	if _, ok, _ := cp.Room(ctx, room.ID); !ok {
		t.Error("la sala debía estar en la copia")
	}

	// B salió de la sala al borrar: A lo sabe y sus mensajes ya no le llegan.
	waitFor(t, a, "A sabe que B salió", chatEvent(chat.MessageReceived, "Beto salió de la sala"))
	if r, _, _ := a.app.Room(ctx, room.ID); len(r.Members) != 1 {
		t.Errorf("sala en A: %+v", r.Room)
	}
	if _, err := a.app.SendRoom(ctx, room.ID, "solo A queda"); err != nil {
		t.Fatal(err)
	}

	// Sigue funcionando después de borrar.
	if _, err := a.app.Send(ctx, idB, "¿sigues ahí?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, "B recibe tras borrar", chatEvent(chat.MessageReceived, "¿sigues ahí?"))
	if rooms, _ := b.app.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("la sala no debía volver a B: %+v", rooms)
	}
	if h, _ := b.app.RoomHistory(ctx, room.ID, "", 10); len(h) != 0 {
		t.Errorf("el aviso de salida ya entregado debía borrarse: %+v", h)
	}
}
