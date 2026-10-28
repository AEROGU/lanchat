package chat

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/identity"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
)

// postOps entrega avisos como si llegaran por TLS con la identidad id.
func postOps(t *testing.T, s *Service, from string, id *identity.Identity, ops ...wireOp) int {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"from": from, "ops": ops})
	req := httptest.NewRequest(http.MethodPost, protocol.RouteOps, strings.NewReader(string(b)))
	req.RemoteAddr = "192.168.1.30:51234"
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{id.Cert.Leaf}}
	rec := httptest.NewRecorder()
	s.handleOps(rec, req)
	return rec.Code
}

func TestAuthorizeRoomChange(t *testing.T) {
	// "a" administra; "b" y "c" son miembros.
	local := store.Room{Members: []string{"a", "b", "c"}, Admins: []string{"a"}, Version: 3}
	cases := []struct {
		name    string
		from    string
		members []string
		admins  []string
		ok      bool
	}{
		{"un miembro agrega a otro", "b", []string{"a", "b", "c", "d"}, []string{"a"}, true},
		{"un miembro sale", "b", []string{"a", "c"}, []string{"a"}, true},
		{"un miembro saca a otro", "b", []string{"a", "b"}, []string{"a"}, false},
		{"un miembro saca al administrador", "b", []string{"b", "c"}, nil, false},
		{"un miembro se hace administrador", "b", []string{"a", "b", "c"}, []string{"a", "b"}, false},
		{"un miembro le quita el privilegio al administrador", "b", []string{"a", "b", "c"}, []string{"b"}, false},
		{"el administrador saca a un miembro", "a", []string{"a", "b"}, []string{"a"}, true},
		{"el administrador nombra a otro", "a", []string{"a", "b", "c"}, []string{"a", "c"}, true},
		{"el administrador sale y hereda el más antiguo", "a", []string{"b", "c"}, nil, true},
		{"alguien agregado en una versión que no llegó", "x", []string{"a", "b", "c", "x"}, []string{"a"}, true},
	}
	for _, c := range cases {
		next := store.Room{Members: c.members, Admins: c.admins, Version: 4}
		if err := authorizeRoomChange(local, next, c.from); (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	// Una sala de una versión anterior no tiene administradores guardados:
	// administra su primer miembro.
	old := store.Room{Members: []string{"a", "b", "c"}, Version: 3}
	if err := authorizeRoomChange(old, store.Room{Members: []string{"a", "b"}}, "a"); err != nil {
		t.Errorf("el primer miembro administra una sala anterior: %v", err)
	}
	if err := authorizeRoomChange(old, store.Room{Members: []string{"a", "b"}}, "b"); err == nil {
		t.Error("b no administra una sala anterior")
	}
}

// Sacar a este equipo de una sala solo lo puede hacer un administrador.
func TestKickNeedsAdmin(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	other := mustIdentity("tercero")
	// "otro" creó la sala y la administra.
	r := wireRoom{ID: "sala1", Name: "Proyecto", Members: []string{"otro", "yo", "tercero"}, Admins: []string{"otro"}, Version: 1}
	if code := post(t, s, roomMsg("m1", "otro", r)); code != http.StatusNoContent {
		t.Fatalf("crear: %d", code)
	}
	without := wireRoom{ID: "sala1", Name: "Proyecto", Members: []string{"otro", "tercero"}, Admins: []string{"otro"}, Version: 2}
	if code := postAs(t, s, roomMsg("m2", "tercero", without), other); code != http.StatusForbidden {
		t.Errorf("un miembro cualquiera saca a este equipo: %d", code)
	}
	if room, _, _ := s.store.Room(ctx, "sala1"); room.Left || len(room.Members) != 3 {
		t.Errorf("la sala no debía cambiar: %+v", room)
	}
	kick := roomMsg("m3", "otro", without)
	kick.Event, kick.Body = true, "Otro sacó a Yo de la sala"
	if code := post(t, s, kick); code != http.StatusNoContent {
		t.Fatalf("el administrador saca a este equipo: %d", code)
	}
	room, _, _ := s.store.Room(ctx, "sala1")
	if !room.Left || len(room.Members) != 2 {
		t.Errorf("este equipo debía quedar fuera: %+v", room)
	}
	// El aviso de que lo sacaron sí se guarda; lo que llegue después, no.
	h, _ := s.store.RoomHistory(ctx, "sala1", "", 10)
	if len(h) != 2 || h[1].Body != "Otro sacó a Yo de la sala" {
		t.Errorf("historial: %+v", h)
	}
	without.Version = 3
	if code := post(t, s, roomMsg("m4", "otro", without)); code != http.StatusForbidden {
		t.Errorf("mensaje a quien ya no es miembro: %d", code)
	}
	if h, _ := s.store.RoomHistory(ctx, "sala1", "", 10); len(h) != 2 {
		t.Errorf("no debía guardarse nada más: %d", len(h))
	}
	// Una versión anterior no manda administradores: se conservan los que había.
	s2 := newTestService(t)
	post(t, s2, roomMsg("n1", "otro", r))
	legacy := wireRoom{ID: "sala1", Name: "Renombrada", Members: []string{"otro", "yo", "tercero"}, Version: 2}
	if code := postAs(t, s2, roomMsg("n2", "tercero", legacy), other); code != http.StatusNoContent {
		t.Fatalf("renombrar desde una versión anterior: %d", code)
	}
	if room, _, _ := s2.store.Room(ctx, "sala1"); room.Name != "Renombrada" || !room.IsAdmin("otro") || room.IsAdmin("tercero") {
		t.Errorf("administradores tras el cambio de una versión anterior: %+v", room)
	}
}

func TestHandleOps(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	stranger := mustIdentity("extraño")
	sent := time.Now().Add(-time.Minute)
	in := store.Message{ID: "m1", PeerID: "otro", Body: "hola", At: sent, SentAt: sent, Status: store.StatusDelivered, Unread: true}
	mine := store.Message{ID: "m2", PeerID: "otro", Outgoing: true, Body: "mío", At: sent, SentAt: sent, Status: store.StatusDelivered}
	old := store.Message{ID: "m3", PeerID: "otro", Body: "viejo", At: sent, SentAt: sent.Add(-time.Hour), Status: store.StatusDelivered}
	for _, m := range []store.Message{in, mine, old} {
		s.store.InsertMessage(ctx, m)
	}
	s.store.UpsertPeer(ctx, store.Peer{ID: "otro", Fingerprint: senderID.Fingerprint})
	now := time.Now().UnixMilli()
	body := func(id string) string {
		m, _, _ := s.store.Message(ctx, id)
		return m.Body
	}
	ignored := map[string]struct {
		from string
		id   *identity.Identity
		op   wireOp
	}{
		"otro equipo elimina un mensaje ajeno":   {"extraño", stranger, wireOp{Type: protocol.OpDelete, Msg: "m1", At: now}},
		"eliminar un mensaje de este equipo":     {"otro", senderID, wireOp{Type: protocol.OpDelete, Msg: "m2", At: now}},
		"eliminar fuera de plazo":                {"otro", senderID, wireOp{Type: protocol.OpDelete, Msg: "m3", At: now}},
		"reaccionar a una conversación ajena":    {"extraño", stranger, wireOp{Type: protocol.OpReact, Msg: "m1", Emoji: "👍", At: now}},
		"reaccionar con texto":                   {"otro", senderID, wireOp{Type: protocol.OpReact, Msg: "m1", Emoji: "hola", At: now}},
		"mensaje desconocido":                    {"otro", senderID, wireOp{Type: protocol.OpDelete, Msg: "nada", At: now}},
		"tipo de aviso de una versión posterior": {"otro", senderID, wireOp{Type: "editar", Msg: "m1", At: now}},
	}
	for name, c := range ignored {
		// Se responde 204 (reintentar no serviría) pero nada cambia.
		if code := postOps(t, s, c.from, c.id, c.op); code != http.StatusNoContent {
			t.Errorf("%s: código %d", name, code)
		}
	}
	for id, want := range map[string]string{"m1": "hola", "m2": "mío", "m3": "viejo"} {
		if m, _, _ := s.store.Message(ctx, id); m.Body != want || m.Deleted || len(m.Reactions) != 0 {
			t.Errorf("%s no debía cambiar: %+v", id, m)
		}
	}
	// Suplantar a "otro" con otra identidad TLS: se rechaza la tanda entera.
	if code := postOps(t, s, "otro", stranger, wireOp{Type: protocol.OpDelete, Msg: "m1", At: now}); code != http.StatusForbidden {
		t.Errorf("suplantación: %d", code)
	}

	// Lo legítimo: reaccionar (también a un mensaje nuestro) y eliminar el propio.
	if code := postOps(t, s, "otro", senderID,
		wireOp{Type: protocol.OpReact, Msg: "m2", Emoji: "❤️", At: now},
		wireOp{Type: protocol.OpReact, Msg: "m1", Emoji: "👍", At: now},
		wireOp{Type: protocol.OpDelete, Msg: "m1", At: now},
	); code != http.StatusNoContent {
		t.Fatalf("avisos válidos: %d", code)
	}
	if m, _, _ := s.store.Message(ctx, "m2"); len(m.Reactions) != 1 || m.Reactions[0].PeerID != "otro" || m.Reactions[0].Emoji != "❤️" {
		t.Errorf("reacción a nuestro mensaje: %+v", m.Reactions)
	}
	if m, _, _ := s.store.Message(ctx, "m1"); !m.Deleted || body("m1") != "" || m.Unread || len(m.Reactions) != 0 {
		t.Errorf("mensaje eliminado: %+v", m)
	}

	// Este equipo solo elimina para todos lo suyo, de texto y dentro del plazo.
	if _, err := s.DeleteForEveryone(ctx, "m3"); err == nil {
		t.Error("no se elimina un mensaje ajeno")
	}
	late := store.Message{ID: "m4", PeerID: "otro", Outgoing: true, Body: "tarde", At: sent,
		SentAt: time.Now().Add(-protocol.DeleteWindow - time.Minute), Status: store.StatusDelivered}
	s.store.InsertMessage(ctx, late)
	if _, err := s.DeleteForEveryone(ctx, "m4"); err == nil {
		t.Error("fuera de plazo no se elimina")
	}
	if _, err := s.DeleteForEveryone(ctx, "m2"); err != nil {
		t.Errorf("eliminar el propio: %v", err)
	}
	if ops, _ := s.store.PendingOps(ctx, "otro", 10); len(ops) != 1 || !strings.Contains(ops[0].Payload, `"type":"delete"`) {
		t.Errorf("aviso pendiente para el otro equipo: %+v", ops)
	}
}

// Una oferta de archivos se elimina para todos cuando ya terminó: quien la
// envió, descargada, cancelada, rechazada o caducada (no mientras sigue
// pendiente); quien la recibió, en cualquier estado (lo descargado se queda
// en su carpeta). Se limpia en disco y se borra con sus miniaturas.
func TestDeleteFilesForEveryone(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	var discarded []string
	s.SetDiscardFiles(func(_ context.Context, id string) { discarded = append(discarded, id) })
	s.store.UpsertPeer(ctx, store.Peer{ID: "otro", Fingerprint: senderID.Fingerprint})
	sent := time.Now().Add(-time.Minute)
	offer := func(id string, outgoing bool, st store.TransferState) {
		t.Helper()
		m := store.Message{ID: id, PeerID: "otro", Outgoing: outgoing, Body: "📎 foto.jpg", Kind: store.KindFiles,
			At: sent, SentAt: sent, Status: store.StatusDelivered}
		tr := store.Transfer{ID: id, PeerID: "otro", Outgoing: outgoing, State: st, ExpiresAt: time.Now().Add(time.Hour),
			Files: []store.TransferFile{{Name: "foto.jpg", Size: 10, Thumb: []byte("\xff\xd8 miniatura")}}}
		if _, err := s.store.InsertMessageWithTransfer(ctx, m, &tr); err != nil {
			t.Fatal(err)
		}
	}
	gone := func(id string) bool {
		m, _, _ := s.store.Message(ctx, id)
		_, ok, _ := s.store.Transfer(ctx, id)
		return m.Deleted && !ok
	}

	// Enviadas: la pendiente no; la completada y la cancelada sí.
	offer("pendiente", true, store.TransferOffered)
	offer("completa", true, store.TransferCompleted)
	offer("cancelada", true, store.TransferCanceled)
	if _, err := s.DeleteForEveryone(ctx, "pendiente"); err == nil {
		t.Error("eliminar para todos una oferta pendiente debía fallar")
	}
	for _, id := range []string{"completa", "cancelada"} {
		if _, err := s.DeleteForEveryone(ctx, id); err != nil || !gone(id) {
			t.Errorf("eliminar %s: %v", id, err)
		}
	}
	if ops, _ := s.store.PendingOps(ctx, "otro", 10); len(ops) != 2 {
		t.Errorf("avisos pendientes para el otro equipo: %+v", ops)
	}

	// Recibidas: se eliminan la pendiente y la descargada.
	offer("recibida", false, store.TransferOffered)
	offer("descargada", false, store.TransferCompleted)
	now := time.Now().UnixMilli()
	if code := postOps(t, s, "otro", senderID,
		wireOp{Type: protocol.OpDelete, Msg: "recibida", At: now},
		wireOp{Type: protocol.OpDelete, Msg: "descargada", At: now},
	); code != http.StatusNoContent {
		t.Fatalf("avisos: %d", code)
	}
	for _, id := range []string{"recibida", "descargada"} {
		if !gone(id) {
			t.Errorf("%s debía eliminarse", id)
		}
	}
	if n, _, _ := s.store.ThumbsUsage(ctx); n != 1 { // solo la pendiente
		t.Errorf("quedan %d miniaturas, quería 1", n)
	}
	if strings.Join(discarded, ",") != "completa,cancelada,recibida,descargada" {
		t.Errorf("limpiadas en disco: %v", discarded)
	}
}

// Solo el autor edita su mensaje de texto, dentro del plazo; gana la edición
// más reciente.
func TestEditOps(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	stranger := mustIdentity("extraño")
	sent := time.Now().Add(-time.Minute)
	msgs := []store.Message{
		{ID: "m1", PeerID: "otro", Body: "hola", At: sent, SentAt: sent, Status: store.StatusDelivered},
		{ID: "m2", PeerID: "otro", Outgoing: true, Body: "mío", At: sent, SentAt: sent, Status: store.StatusDelivered},
		{ID: "m3", PeerID: "otro", Body: "viejo", At: sent, SentAt: sent.Add(-time.Hour), Status: store.StatusDelivered},
		{ID: "m4", PeerID: "otro", Outgoing: true, Body: "tarde", At: sent,
			SentAt: time.Now().Add(-protocol.DeleteWindow - time.Minute), Status: store.StatusDelivered},
		{ID: "m5", PeerID: "otro", Body: "📎 foto.jpg", Kind: store.KindFiles, At: sent, SentAt: sent, Status: store.StatusDelivered},
	}
	for _, m := range msgs {
		s.store.InsertMessage(ctx, m)
	}
	s.store.UpsertPeer(ctx, store.Peer{ID: "otro", Fingerprint: senderID.Fingerprint})
	now := time.Now().UnixMilli()
	body := func(id string) string {
		m, _, _ := s.store.Message(ctx, id)
		return m.Body
	}

	ignored := map[string]struct {
		from string
		id   *identity.Identity
		op   wireOp
	}{
		"otro equipo edita un mensaje ajeno": {"extraño", stranger, wireOp{Type: protocol.OpEdit, Msg: "m1", Body: "x", At: now}},
		"editar un mensaje de este equipo":   {"otro", senderID, wireOp{Type: protocol.OpEdit, Msg: "m2", Body: "x", At: now}},
		"editar fuera de plazo":              {"otro", senderID, wireOp{Type: protocol.OpEdit, Msg: "m3", Body: "x", At: now}},
		"editar una oferta de archivos":      {"otro", senderID, wireOp{Type: protocol.OpEdit, Msg: "m5", Body: "x", At: now}},
		"dejarlo vacío":                      {"otro", senderID, wireOp{Type: protocol.OpEdit, Msg: "m1", Body: "  ", At: now}},
		"con caracteres de control":          {"otro", senderID, wireOp{Type: protocol.OpEdit, Msg: "m1", Body: "a\x1b[2J", At: now}},
	}
	for name, c := range ignored {
		if code := postOps(t, s, c.from, c.id, c.op); code != http.StatusNoContent {
			t.Errorf("%s: código %d", name, code)
		}
	}
	for _, m := range msgs {
		if got, _, _ := s.store.Message(ctx, m.ID); got.Body != m.Body || !got.EditedAt.IsZero() {
			t.Errorf("%s no debía cambiar: %+v", m.ID, got)
		}
	}

	// La edición legítima, y una atrasada que no la pisa.
	if code := postOps(t, s, "otro", senderID,
		wireOp{Type: protocol.OpEdit, Msg: "m1", Body: "hola, corregido", At: now},
		wireOp{Type: protocol.OpEdit, Msg: "m1", Body: "atrasada", At: now - 1000},
	); code != http.StatusNoContent {
		t.Fatalf("edición: %d", code)
	}
	if m, _, _ := s.store.Message(ctx, "m1"); m.Body != "hola, corregido" || m.EditedAt.UnixMilli() != now {
		t.Errorf("mensaje editado: %+v", m)
	}
	// Una edición del largo máximo cabe en una tanda, aunque al escaparla en
	// JSON ocupe varias veces más.
	long := strings.Repeat("<", protocol.MaxMessageBytes)
	if code := postOps(t, s, "otro", senderID, wireOp{Type: protocol.OpEdit, Msg: "m1", Body: long, At: now + 1}); code != http.StatusNoContent {
		t.Fatalf("edición larga: %d", code)
	}
	if body("m1") != long {
		t.Error("la edición larga no se aplicó")
	}

	// Este equipo edita lo suyo, de texto y dentro del plazo.
	for _, id := range []string{"m1", "m4", "m5"} {
		if _, err := s.Edit(ctx, id, "nuevo"); err == nil {
			t.Errorf("editar %s debía fallar", id)
		}
	}
	if _, err := s.Edit(ctx, "m2", " "); err == nil {
		t.Error("no se deja vacío")
	}
	if m, err := s.Edit(ctx, "m2", "mío"); err != nil || !m.EditedAt.IsZero() {
		t.Errorf("sin cambios no es una edición: %+v, %v", m, err)
	}
	m, err := s.Edit(ctx, "m2", "mío, corregido")
	if err != nil || m.Body != "mío, corregido" || m.EditedAt.IsZero() {
		t.Fatalf("editar el propio: %+v, %v", m, err)
	}
	ops, _ := s.store.PendingOps(ctx, "otro", 10)
	if len(ops) != 1 || !strings.Contains(ops[0].Payload, `"type":"edit"`) || !strings.Contains(ops[0].Payload, "mío, corregido") {
		t.Errorf("aviso pendiente para el otro equipo: %+v", ops)
	}
}

// Las tandas de avisos no pasan de protocol.OpsBatchBytes, salvo un aviso
// que solo ya es más grande.
func TestBatchLen(t *testing.T) {
	op := func(n int) store.Op { return store.Op{Payload: strings.Repeat("x", n)} }
	big := protocol.OpsBatchBytes + 1
	cases := []struct {
		ops  []store.Op
		want int
	}{
		{[]store.Op{op(10), op(10), op(10)}, 3},
		{[]store.Op{op(big), op(10)}, 1},
		{[]store.Op{op(10), op(big)}, 1},
		{[]store.Op{op(protocol.OpsBatchBytes / 2), op(protocol.OpsBatchBytes / 2), op(10)}, 1},
	}
	for i, c := range cases {
		if got := batchLen(c.ops); got != c.want {
			t.Errorf("caso %d: %d avisos, quería %d", i, got, c.want)
		}
	}
}
