package ui

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AEROGU/lanchat/internal/store"
)

// Compartir (Android): archivos sueltos tal cual y cada carpeta como .zip;
// solo lo recibido y completado.
func TestShareFiles(t *testing.T) {
	recv := t.TempDir()
	write := func(rel, data string) string {
		p := filepath.Join(recv, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := write("a.txt", "A"), write("b.jpg", "B")
	plano := write("Obra (1)/planos/p.txt", "plano")
	nota := write("Obra (1)/nota.txt", "nota")
	testTransfers["s1"] = store.Transfer{ID: "s1", PeerID: "c1", State: store.TransferCompleted, Dir: recv,
		Files: []store.TransferFile{
			{Index: 0, Name: "a.txt", Done: true, Path: a},
			{Index: 1, Name: "b.jpg", Done: true, Path: b},
			{Index: 2, Name: "p.txt", Dir: "Obra (1)/planos", Done: true, Path: plano},
			{Index: 3, Name: "nota.txt", Dir: "Obra (1)", Done: true, Path: nota},
			{Index: 4, Name: "falta.txt", Dir: "Obra (1)", Done: false},
		}}
	// Enviadas: se comparten aunque el contacto no las haya descargado, si
	// siguen en el equipo (las copias temporales se borran al terminar).
	testTransfers["s2"] = store.Transfer{ID: "s2", PeerID: "c1", Outgoing: true, State: store.TransferOffered,
		Files: []store.TransferFile{
			{Index: 0, Name: "a.txt", Path: a},
			{Index: 1, Name: "p.txt", Dir: "Obra/planos", Path: plano},
		}}
	testTransfers["s3"] = store.Transfer{ID: "s3", PeerID: "c1", Outgoing: true, State: store.TransferCompleted,
		Files: []store.TransferFile{{Index: 0, Name: "x.txt", Done: true, Path: filepath.Join(recv, "borrado.txt")}}}
	t.Cleanup(func() {
		for _, id := range []string{"s1", "s2", "s3"} {
			delete(testTransfers, id)
		}
	})
	for id, want := range map[string]bool{"s1": true, "s2": true, "s3": false} {
		if got := shareable(testTransfers[id]); got != want {
			t.Errorf("shareable(%s) = %v", id, got)
		}
	}

	shareDir := t.TempDir()
	// Un .zip viejo se borra al preparar otro.
	old := filepath.Join(shareDir, "viejo")
	os.MkdirAll(old, 0o700)
	past := time.Now().Add(-2 * shareMaxAge)
	os.Chtimes(old, past, past)

	var mu sync.Mutex
	var shared [][]string
	s, _ := startServer(t, func(s *Server) {
		s.Shell = &Shell{
			OpenFile: func(string) error { return nil },
			OpenURL:  func(string) error { return nil },
			ShareFiles: func(p []string) error {
				mu.Lock()
				defer mu.Unlock()
				shared = append(shared, p)
				return nil
			},
		}
		s.ShareDir = shareDir
	})
	c := loggedClient(t, s)

	cases := []struct {
		body string
		want int
	}{
		{`{"id":"s1","indexes":[0,1]}`, http.StatusNoContent},
		{`{"id":"s1","dirs":["Obra (1)"]}`, http.StatusNoContent},
		{`{"id":"s1","indexes":[4]}`, http.StatusBadRequest},   // sin terminar
		{`{"id":"s1","indexes":[9]}`, http.StatusBadRequest},   // no existe
		{`{"id":"s1","indexes":[]}`, http.StatusBadRequest},    // nada que compartir
		{`{"id":"s1","dirs":["Otra"]}`, http.StatusBadRequest}, // carpeta ajena
		{`{"id":"s2","indexes":[0]}`, http.StatusNoContent},    // enviada
		{`{"id":"s2","dirs":["Obra"]}`, http.StatusNoContent},  // carpeta enviada
		{`{"id":"s3","indexes":[0]}`, http.StatusBadRequest},   // enviada y ya borrada
		{`{"id":"nada","indexes":[0]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp, err := postJSON(c, base(s)+"/api/files/share", tc.body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %d, quería %d", tc.body, resp.StatusCode, tc.want)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(shared) != 4 || !slices.Equal(shared[0], []string{a, b}) || !slices.Equal(shared[2], []string{a}) {
		t.Fatalf("compartidos = %v", shared)
	}
	if want := filepath.Join(shareDir, "s2", "Obra.zip"); shared[3][0] != want {
		t.Errorf("zip enviado = %s, quería %s", shared[3][0], want)
	}
	zipPath := shared[1][0]
	if want := filepath.Join(shareDir, "s1", "Obra (1).zip"); zipPath != want {
		t.Errorf("zip = %s, quería %s", zipPath, want)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "Obra (1)/planos/p.txt,Obra (1)/nota.txt" {
		t.Errorf("contenido del zip = %s", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("el .zip viejo debía borrarse")
	}

	// En la PC se copian al portapapeles, con las mismas reglas.
	var copyMu sync.Mutex
	var copied [][]string
	clipboardCopy = func(p []string) error {
		copyMu.Lock()
		defer copyMu.Unlock()
		copied = append(copied, p)
		return nil
	}
	t.Cleanup(func() { clipboardCopy = copyFiles })
	pcDir := t.TempDir()
	pc, _ := startServer(t, func(s *Server) { s.ShareDir = pcDir })
	pcClient := loggedClient(t, pc)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"id":"s1","indexes":[0],"dirs":["Obra (1)"]}`, http.StatusNoContent},
		{`{"id":"s1","indexes":[0,1]}`, http.StatusNoContent},
		{`{"id":"s2","indexes":[0]}`, http.StatusNoContent},
		{`{"id":"s3","indexes":[0]}`, http.StatusBadRequest},
	} {
		resp, err := postJSON(pcClient, base(pc)+"/api/files/share", tc.body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("PC %s: %d, quería %d", tc.body, resp.StatusCode, tc.want)
		}
	}
	want := [][]string{{a, filepath.Join(pcDir, "s1", "Obra (1).zip")}, {a, b}, {a}}
	copyMu.Lock()
	defer copyMu.Unlock()
	if !slices.EqualFunc(copied, want, slices.Equal) {
		t.Errorf("copiados = %v, quería %v", copied, want)
	}
}
