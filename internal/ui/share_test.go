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
	testTransfers["s2"] = store.Transfer{ID: "s2", PeerID: "c1", Outgoing: true, State: store.TransferCompleted,
		Files: []store.TransferFile{{Index: 0, Name: "a.txt", Done: true, Path: a}}}
	t.Cleanup(func() { delete(testTransfers, "s1"); delete(testTransfers, "s2") })

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
			ShareDir: shareDir,
		}
	})
	c := loggedClient(t, s)

	cases := []struct {
		body string
		want int
	}{
		{`{"id":"s1","indexes":[0,1]}`, http.StatusNoContent},
		{`{"id":"s1","dir":"Obra (1)"}`, http.StatusNoContent},
		{`{"id":"s1","indexes":[4]}`, http.StatusBadRequest}, // sin terminar
		{`{"id":"s1","indexes":[9]}`, http.StatusBadRequest}, // no existe
		{`{"id":"s1","indexes":[]}`, http.StatusBadRequest},  // nada que compartir
		{`{"id":"s1","dir":"Otra"}`, http.StatusBadRequest},  // carpeta ajena
		{`{"id":"s2","indexes":[0]}`, http.StatusBadRequest}, // enviada, no recibida
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
	if len(shared) != 2 || !slices.Equal(shared[0], []string{a, b}) {
		t.Fatalf("compartidos = %v", shared)
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

	// En la PC todavía no hay "Compartir".
	pc, _ := startServer(t)
	resp, _ := postJSON(loggedClient(t, pc), base(pc)+"/api/files/share", `{"id":"s1","indexes":[0]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("sin Shell: %d", resp.StatusCode)
	}
}
