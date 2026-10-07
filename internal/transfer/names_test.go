package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	if got := offerSummary([]string{"a.pdf"}, 1500); got != "📎 a.pdf (1.5 KB)" {
		t.Errorf("uno: %q", got)
	}
	got := offerSummary([]string{"a", "b", "c", "d"}, 100)
	if got != "📎 4 archivos (100 B): a, b, c…" {
		t.Errorf("varios: %q", got)
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
