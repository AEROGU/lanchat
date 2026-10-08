package chat

import (
	"context"
	"net/http"
	"testing"
)

func roomMsg(id, from string, r wireRoom) wireMessage {
	return wireMessage{ID: id, From: from, Body: "hola", Room: &r}
}

// Solo los miembros escriben en una sala, y solo una versión más nueva la cambia.
func TestHandleRoomMessage(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	other := mustIdentity("tercero")
	r := wireRoom{ID: "sala1", Name: "Proyecto", Members: []string{"otro", "yo"}, Version: 2}

	codes := map[string]struct {
		m    wireMessage
		want int
	}{
		"sin este equipo":   {roomMsg("x1", "otro", wireRoom{ID: "sala1", Name: "P", Members: []string{"otro"}, Version: 1}), http.StatusForbidden},
		"miembro repetido":  {roomMsg("x2", "otro", wireRoom{ID: "sala1", Name: "P", Members: []string{"otro", "yo", "yo"}, Version: 1}), http.StatusBadRequest},
		"sin versión":       {roomMsg("x3", "otro", wireRoom{ID: "sala1", Name: "P", Members: []string{"otro", "yo"}}), http.StatusBadRequest},
		"remitente ausente": {roomMsg("x4", "otro", wireRoom{ID: "sala1", Name: "P", Members: []string{"tercero", "yo"}, Version: 1}), http.StatusForbidden},
	}
	for name, c := range codes {
		if code := post(t, s, c.m); code != c.want {
			t.Errorf("%s: %d, quería %d", name, code, c.want)
		}
	}

	if code := post(t, s, roomMsg("m1", "otro", r)); code != http.StatusNoContent {
		t.Fatalf("mensaje de un miembro: %d", code)
	}
	if h, _ := s.store.RoomHistory(ctx, "sala1", "", 10); len(h) != 1 || h[0].PeerID != "otro" || !h[0].Unread {
		t.Errorf("historial: %+v", h)
	}

	// Un tercero que no es miembro aquí no puede agregarse con una versión vieja…
	intruder := r
	intruder.Members = []string{"otro", "yo", "tercero"}
	if code := postAs(t, s, roomMsg("m2", "tercero", intruder), other); code != http.StatusForbidden {
		t.Errorf("intruso con versión vieja: %d", code)
	}
	// …pero sí si otro miembro lo agregó y aquí aún no llegaba el cambio.
	intruder.Version = 3
	if code := postAs(t, s, roomMsg("m3", "tercero", intruder), other); code != http.StatusNoContent {
		t.Errorf("miembro nuevo: %d", code)
	}
	if room, _, _ := s.store.Room(ctx, "sala1"); len(room.Members) != 3 || room.Version != 3 {
		t.Errorf("sala: %+v", room)
	}

	// Una versión vieja no deshace el cambio.
	if code := post(t, s, roomMsg("m4", "otro", r)); code != http.StatusNoContent {
		t.Errorf("versión vieja: %d", code)
	}
	if room, _, _ := s.store.Room(ctx, "sala1"); room.Version != 3 {
		t.Errorf("la versión vieja no debía aplicarse: %+v", room)
	}

	// Tras salir de la sala no se guardan más mensajes.
	room, _, _ := s.store.Room(ctx, "sala1")
	room.Left, room.Members = true, []string{"otro", "tercero"}
	room.Version++
	if err := s.store.SaveRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	// Se responde 204 para que el remitente no reintente.
	if code := post(t, s, roomMsg("m5", "otro", r)); code != http.StatusNoContent {
		t.Errorf("tras salir: %d", code)
	}
	if h, _ := s.store.RoomHistory(ctx, "sala1", "", 10); len(h) != 3 {
		t.Errorf("historial tras salir: %d mensajes", len(h))
	}
	if _, err := s.SendRoom(ctx, "sala1", "hola", false); err == nil {
		t.Error("no debía poder escribir tras salir")
	}
}
