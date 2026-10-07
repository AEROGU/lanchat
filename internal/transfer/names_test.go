package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AEROGU/lanchat/internal/store"
)

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"informe.pdf":         "informe.pdf",
		"año 2026 – ñ.xlsx":   "año 2026 – ñ.xlsx",
		`..\..\Windows\x.dll`: ".._.._Windows_x.dll",
		"a<b>c:d|e?f*.txt":    "a_b_c_d_e_f_.txt",
		"CON":                 "_CON",
		"con.txt":             "_con.txt",
		"LPT1.log":            "_LPT1.log",
		"console.txt":         "console.txt",
		"nombre. . .":         "nombre",
		"   ":                 "archivo",
		"\x00\x1f":            "__",
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, quería %q", in, got, want)
		}
	}
}

func TestTruncateNameKeepsExtensionAndUTF8(t *testing.T) {
	long := strings.Repeat("ñ", 150) + ".pdf" // 304 bytes
	got := truncateName(long, maxSavedNameBytes)
	if len(got) > maxSavedNameBytes || !strings.HasSuffix(got, ".pdf") || !utf8.ValidString(got) {
		t.Errorf("truncateName = %q (%d bytes)", got, len(got))
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"foto.jpg", "foto (1).jpg"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o600)
	}
	got, err := uniquePath(dir, "foto.jpg")
	if err != nil || filepath.Base(got) != "foto (2).jpg" {
		t.Errorf("uniquePath = %q, %v", got, err)
	}
}

func TestOfferSummaryAndSize(t *testing.T) {
	if got := humanSize(2_400_000); got != "2.3 MB" {
		t.Errorf("humanSize = %q", got)
	}
	files := func(specs ...string) []store.TransferFile { // "dir|nombre"
		var out []store.TransferFile
		for _, s := range specs {
			dir, name, _ := strings.Cut(s, "|")
			out = append(out, store.TransferFile{Name: name, Dir: dir})
		}
		return out
	}
	cases := []struct {
		files []store.TransferFile
		want  string
	}{
		{files("|a.pdf"), "📎 a.pdf (1.5 KB)"},
		{files("|a", "|b", "|c", "|d"), "📎 4 elementos (1.5 KB): a, b, c…"},
		{files("Proyecto|a", "Proyecto/planos|b"), "📁 Proyecto (2 archivos, 1.5 KB)"},
		{files("Proyecto|a", "|nota.txt"), "📎 2 elementos (1.5 KB): Proyecto/, nota.txt"},
	}
	for _, c := range cases {
		if got := offerSummary(c.files, 1500); got != c.want {
			t.Errorf("offerSummary = %q, quería %q", got, c.want)
		}
	}
}

func TestExpandPathsAndLocalDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Proyecto")
	os.MkdirAll(filepath.Join(root, "planos", "vacía"), 0o700)
	os.WriteFile(filepath.Join(root, "leeme.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "planos", "p1.dwg"), []byte("x"), 0o600)
	loose := filepath.Join(t.TempDir(), "suelto.txt")
	os.WriteFile(loose, []byte("x"), 0o600)

	items, err := ExpandPaths([]string{root, loose})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Dir+"|"+filepath.Base(it.Path))
	}
	if strings.Join(got, ",") != "Proyecto|leeme.txt,Proyecto/planos|p1.dwg,|suelto.txt" {
		t.Errorf("ExpandPaths = %v", got)
	}

	// Si "Proyecto" ya existe en descargas, la nueva carpeta es "Proyecto (1)".
	dest := t.TempDir()
	os.Mkdir(filepath.Join(dest, "Proyecto"), 0o700)
	dirs, err := localDirs([]store.TransferFile{
		{Index: 0, Dir: "Proyecto"}, {Index: 1, Dir: "Proyecto/planos"}, {Index: 2}, {Index: 3, Dir: "Otra:carpeta/x"},
	}, dest)
	if err != nil || dirs[0] != "Proyecto (1)" || dirs[1] != "Proyecto (1)/planos" || dirs[2] != "" || dirs[3] != "Otra_carpeta/x" {
		t.Errorf("localDirs = %v, %v", dirs, err)
	}
}

func TestParseRange(t *testing.T) {
	if n, err := parseRange("", 10); n != 0 || err != nil {
		t.Error("sin Range")
	}
	if n, err := parseRange("bytes=4-", 10); n != 4 || err != nil {
		t.Error("bytes=4-")
	}
	for _, bad := range []string{"bytes=11-", "bytes=0-5", "bytes=-5", "items=1-"} {
		if _, err := parseRange(bad, 10); err == nil {
			t.Errorf("%q debía rechazarse", bad)
		}
	}
}
