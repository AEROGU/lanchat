package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/testutil"
)

// onDisk indica si text aparece en el archivo de la base o en su WAL.
func onDisk(t *testing.T, path, text string) bool {
	t.Helper()
	for _, p := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(text)) {
			return true
		}
	}
	return false
}

func TestPrivacy(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "lanchat.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"a", "b"} {
		if err := s.UpsertPeer(ctx, Peer{ID: id, Hostname: "PC-" + id, Fingerprint: "huella-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	s.SetAlias(ctx, "a", "Jefa")
	s.SetGroup(ctx, "a", "Ventas")
	msg := func(id, peer, room, body string) Message {
		return Message{ID: id, PeerID: peer, RoomID: room, Body: body, At: now, SentAt: now, Status: StatusDelivered}
	}
	s.InsertMessage(ctx, msg("m1", "a", "", "SECRETO-UNO-con-a"))
	s.InsertMessage(ctx, msg("m2", "b", "", "SECRETO-DOS-con-b"))
	tr := Transfer{ID: "f1", PeerID: "a", State: TransferOffered, Token: "tok", ExpiresAt: now.Add(time.Hour),
		Files: []TransferFile{{Name: "plano-confidencial.pdf", Size: 10}}}
	f := msg("f1", "a", "", "1 archivo")
	f.Kind = KindFiles
	if _, err := s.InsertMessageWithTransfer(ctx, f, &tr); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRoom(ctx, Room{ID: "r1", Name: "Sala", Members: []string{"yo", "b"}, Version: 1}); err != nil {
		t.Fatal(err)
	}
	room := msg("m3", "", "r1", "SECRETO-SALA")
	room.Outgoing = true
	if err := s.InsertRoomMessage(ctx, room, []string{"b"}); err != nil {
		t.Fatal(err)
	}

	// La copia es una base completa que se puede abrir.
	backup := filepath.Join(dir, "copia.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, backup); err == nil {
		t.Error("no debía sobrescribir una copia existente")
	}
	cp, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := cp.History(ctx, "b", "", 10); len(h) != 1 || h[0].Body != "SECRETO-DOS-con-b" {
		t.Errorf("historial en la copia: %+v", h)
	}
	cp.Close()

	// Borrar la conversación con a: desaparece del disco; lo demás sigue.
	if !onDisk(t, path, "SECRETO-UNO-con-a") {
		t.Fatal("la prueba no encuentra el texto antes de borrarlo")
	}
	if err := s.DeleteConversation(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, "a", "", 10); len(h) != 0 {
		t.Errorf("quedaron mensajes con a: %+v", h)
	}
	if _, ok, _ := s.Transfer(ctx, "f1"); ok {
		t.Error("quedó la oferta de archivos")
	}
	for _, text := range []string{"SECRETO-UNO-con-a", "plano-confidencial"} {
		if onDisk(t, path, text) {
			t.Errorf("%q sigue en el disco", text)
		}
	}
	if p, _, _ := s.Peer(ctx, "a"); p.Alias != "Jefa" || p.Fingerprint != "huella-a" {
		t.Errorf("el contacto debía conservarse: %+v", p)
	}
	if h, _ := s.History(ctx, "b", "", 10); len(h) != 1 {
		t.Errorf("la conversación con b no debía tocarse: %+v", h)
	}

	// Vaciar la sala la conserva.
	if err := s.DeleteRoomConversation(ctx, "r1", false); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.RoomHistory(ctx, "r1", "", 10); len(h) != 0 {
		t.Errorf("quedaron mensajes en la sala: %+v", h)
	}
	if pend, _ := s.PendingRoomDeliveries(ctx, "b"); len(pend) != 0 {
		t.Errorf("quedaron entregas pendientes: %+v", pend)
	}
	if _, ok, _ := s.Room(ctx, "r1"); !ok {
		t.Error("la sala debía seguir")
	}
	if onDisk(t, path, "SECRETO-SALA") {
		t.Error("el mensaje de la sala sigue en el disco")
	}
	// Mientras se es miembro, hide no la oculta.
	if err := s.DeleteRoomConversation(ctx, "r1", true); err != nil {
		t.Fatal(err)
	}
	if rooms, _ := s.Rooms(ctx); len(rooms) != 1 {
		t.Errorf("la sala no debía ocultarse sin haber salido: %+v", rooms)
	}

	// Tras salir, el aviso pendiente sobrevive al borrado y la sala queda oculta
	// (pero conocida, para ignorar sus mensajes).
	if err := s.SaveRoom(ctx, Room{ID: "r1", Name: "Sala", Members: []string{"b"}, Version: 2, Left: true}); err != nil {
		t.Fatal(err)
	}
	leave := msg("m4", "", "r1", "Yo salí de la sala")
	leave.Outgoing, leave.Kind, leave.Status = true, KindRoomEvent, StatusPending
	if err := s.InsertRoomMessage(ctx, leave, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoomConversation(ctx, "r1", true); err != nil {
		t.Fatal(err)
	}
	if rooms, _ := s.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("la sala debía ocultarse: %+v", rooms)
	}
	if r, ok, _ := s.Room(ctx, "r1"); !ok || !r.Hidden || !r.Left {
		t.Errorf("la sala oculta debía seguir conocida: %+v %v", r, ok)
	}
	if pend, _ := s.PendingRoomDeliveries(ctx, "b"); len(pend) != 1 || pend[0].ID != "m4" {
		t.Fatalf("el aviso de salida debía seguir pendiente: %+v", pend)
	}
	// Una versión nueva que incluye a este equipo (lo volvieron a agregar) la muestra.
	if ok, _ := s.ApplyRoom(ctx, Room{ID: "r1", Name: "Sala", Members: []string{"b", "yo"}, Version: 3}); !ok {
		t.Error("debía aplicarse la versión nueva")
	}
	if rooms, _ := s.Rooms(ctx); len(rooms) != 1 {
		t.Errorf("la sala debía volver a verse: %+v", rooms)
	}

	// Borrar todo: sin mensajes, alias ni grupos; las huellas se conservan.
	// La sala (de la que no se salió) desaparece; el aviso pendiente se
	// conserva hasta entregarse y entonces se borra.
	if err := s.SaveRoom(ctx, Room{ID: "r2", Name: "Otra", Members: []string{"b"}, Version: 2, Left: true}); err != nil {
		t.Fatal(err)
	}
	leave2 := msg("m5", "", "r2", "Yo salí de Otra")
	leave2.Outgoing, leave2.Kind, leave2.Status = true, KindRoomEvent, StatusPending
	if err := s.InsertRoomMessage(ctx, leave2, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if onDisk(t, path, "SECRETO-DOS-con-b") || onDisk(t, path, "Jefa") || onDisk(t, path, "Ventas") {
		t.Error("quedaron datos en el disco tras borrar todo")
	}
	if p, _, _ := s.Peer(ctx, "a"); p.Alias != "" || p.Group != "" || p.Fingerprint != "huella-a" {
		t.Errorf("contacto tras borrar todo: %+v", p)
	}
	if n, _ := s.UnreadCounts(ctx); len(n) != 0 {
		t.Errorf("no leídos: %v", n)
	}
	if rooms, _ := s.Rooms(ctx); len(rooms) != 0 {
		t.Errorf("salas visibles tras borrar todo: %+v", rooms)
	}
	if _, ok, _ := s.Room(ctx, "r1"); ok {
		t.Error("la sala de la que no se salió debía borrarse")
	}
	pend, _ := s.PendingRoomDeliveries(ctx, "b")
	if len(pend) != 2 {
		t.Fatalf("los avisos de salida debían seguir pendientes: %+v", pend)
	}
	if _, err := s.MarkRoomDelivered(ctx, "m5", "b"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.RoomHistory(ctx, "r2", "", 10); len(h) != 0 {
		t.Errorf("el aviso entregado de una sala oculta debía borrarse: %+v", h)
	}
	if r, ok, _ := s.Room(ctx, "r2"); !ok || r.Name != "" || len(r.Members) != 0 {
		t.Errorf("de la sala oculta solo debía quedar el ID: %+v %v", r, ok)
	}
	if err := s.Wipe(ctx); err != nil { // para que el WAL quede vacío
		t.Fatal(err)
	}
	if onDisk(t, path, "Otra") {
		t.Error("el nombre de la sala oculta sigue en el disco")
	}
	if _, err := s.MarkRoomDelivered(ctx, "m4", "b"); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.RoomHistory(ctx, "r1", "", 10); len(h) != 0 {
		t.Errorf("el aviso entregado de una sala borrada debía borrarse: %+v", h)
	}
}

// Quitar un contacto combina su conversación con la de otro (la misma persona
// con otra identidad) o la borra; vuelve a la lista si se anuncia de nuevo.
func TestRemovePeer(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	for _, id := range []string{"vieja", "nueva", "otra", "tercera"} {
		if err := s.UpsertPeer(ctx, Peer{ID: id, Name: id, Hostname: "OPPO", IP: "10.0.0.1", LastSeen: now}); err != nil {
			t.Fatal(err)
		}
	}
	s.SetAlias(ctx, "vieja", "Angelika")
	s.SetGroup(ctx, "vieja", "Oficina")
	s.SetGroup(ctx, "nueva", "Amigos")
	msg := func(id, peer string, at time.Time) Message {
		return Message{ID: id, PeerID: peer, Body: id, At: at, SentAt: at, Status: StatusDelivered, Unread: true}
	}
	s.InsertMessage(ctx, msg("m1", "vieja", now.Add(-2*time.Minute)))
	s.InsertMessage(ctx, msg("m2", "nueva", now.Add(-time.Minute)))
	s.InsertMessage(ctx, msg("m3", "vieja", now))
	s.SetReaction(ctx, "m1", "vieja", "👍", now)
	s.EnqueueOps(ctx, []string{"vieja"}, `{"type":"react"}`)
	s.InsertMessage(ctx, msg("x1", "otra", now))
	s.MarkRead(ctx, "vieja", true) // acuses de lectura pendientes para la vieja

	if err := s.RemovePeer(ctx, "vieja", "nueva"); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(ctx, "nueva", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].ID != "m1" || h[1].ID != "m2" || h[2].ID != "m3" {
		t.Fatalf("conversación combinada: %+v", h)
	}
	// Cada mensaje combinado dice de quién era (el nombre anunciado, no el alias).
	if h[0].MergedFrom != "vieja · OPPO · 10.0.0.1" || h[2].MergedFrom != h[0].MergedFrom || h[1].MergedFrom != "" {
		t.Errorf("origen: %q %q %q", h[0].MergedFrom, h[1].MergedFrom, h[2].MergedFrom)
	}
	if len(h[0].Reactions) != 1 || h[0].Reactions[0].PeerID != "nueva" {
		t.Errorf("reacción: %+v", h[0].Reactions)
	}
	if ids, _ := s.PendingReceipts(ctx, "nueva", 10); len(ids) != 0 {
		t.Errorf("acuses pendientes: %v (eran para la identidad vieja)", ids)
	}
	if u, _ := s.UnreadCounts(ctx); u["nueva"] != 1 || u["vieja"] != 0 {
		t.Errorf("no leídos: %v", u)
	}
	if ops, _ := s.PendingOps(ctx, "vieja", 10); len(ops) != 0 {
		t.Errorf("avisos para la vieja: %v", ops)
	}
	nueva, _, _ := s.Peer(ctx, "nueva")
	if nueva.Alias != "Angelika" || nueva.Group != "Amigos" {
		t.Errorf("hereda el alias pero conserva su grupo: %+v", nueva)
	}
	vieja, ok, _ := s.Peer(ctx, "vieja")
	if !ok || !vieja.Removed || vieja.Alias != "Angelika" {
		t.Errorf("la vieja debía quedar quitada, con su nombre para las salas: %+v", vieja)
	}

	// Combinar otra vez conserva el origen de los que ya venían combinados.
	if err := s.RemovePeer(ctx, "nueva", "tercera"); err != nil {
		t.Fatal(err)
	}
	h, _ = s.History(ctx, "tercera", "", 10)
	if len(h) != 3 || h[0].MergedFrom != "vieja · OPPO · 10.0.0.1" || h[1].MergedFrom != "nueva · OPPO · 10.0.0.1" {
		t.Errorf("segunda combinación: %+v", h)
	}

	// Sin con quién combinar, la conversación se borra.
	if err := s.RemovePeer(ctx, "otra", ""); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.History(ctx, "otra", "", 10); len(h) != 0 {
		t.Errorf("quedaron mensajes: %+v", h)
	}

	// Si vuelve a anunciarse, regresa a la lista.
	if err := s.UpsertPeer(ctx, Peer{ID: "otra", Hostname: "OPPO", LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Peer(ctx, "otra"); p.Removed {
		t.Error("al anunciarse debía volver")
	}
}
