package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndCompare(t *testing.T) {
	for _, s := range []string{"dev", "1286232", "1.2", "v1.2.3.4", ""} {
		if _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) no debería entenderse", s)
		}
	}
	v, ok := Parse("0.19.0-rc.2-6-g1286232-dirty")
	if !ok || v != (Version{0, 19, 0, "rc.2", 6, true}) {
		t.Fatalf("Parse = %+v, %v", v, ok)
	}
	// Cada una es posterior a la anterior.
	order := []string{"0.9.9", "0.19.0-rc.1", "0.19.0-rc.2", "0.19.0", "0.19.0-6-gabc1234", "0.19.1", "v0.20.0", "1.0.0"}
	for i := 1; i < len(order); i++ {
		if !Newer(order[i], order[i-1]) || Newer(order[i-1], order[i]) {
			t.Errorf("se esperaba %s > %s", order[i], order[i-1])
		}
	}
	if Newer("0.20.0", "dev") || Newer("dev", "0.20.0") || Newer("0.19.0", "0.19.0") {
		t.Error("Newer con versiones iguales o sin entender debe ser false")
	}
	for s, want := range map[string]bool{"0.19.0": true, "0.19.0-6-gabc1234": true, "0.19.0-dirty": false, "dev": false} {
		if Updatable(s) != want {
			t.Errorf("Updatable(%q) = %v", s, !want)
		}
	}
}

func TestLatest(t *testing.T) {
	target := "/AEROGU/lanchat/releases/tag/v0.20.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL + "/releases"}

	if v, err := c.Latest(context.Background()); err != nil || v != "0.20.0" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
	target = "/AEROGU/lanchat/releases" // sin versiones publicadas
	if _, err := c.Latest(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("sin versiones: %v", err)
	}
	target = "/AEROGU/lanchat/releases/tag/v0.20.0-rc.1"
	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("una versión preliminar no debería aceptarse como la última")
	}
}

// release sirve los archivos de una Release falsa de la versión 0.20.0.
func release(t *testing.T, files map[string][]byte, sums string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, "/releases/download/v0.20.0/")
		switch {
		case ok && name == ChecksumsName:
			fmt.Fprint(w, sums)
		case ok && files[name] != nil:
			w.Write(files[name])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL + "/releases"}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestFetchChecksSHA256(t *testing.T) {
	data := bytes.Repeat([]byte("lanchat"), 5000)
	other := []byte("otro archivo")
	c := release(t, map[string][]byte{"LanChat-0.20.0.zip": data, "LanChat-0.20.0.apk": data},
		sum(data)+"  LanChat-0.20.0.zip\n"+sum(other)+" *LanChat-0.20.0.apk\n")
	dir := t.TempDir()

	var last int64
	got, err := c.Fetch(context.Background(), "0.20.0", "LanChat-0.20.0.zip", dir, func(done, total int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); !bytes.Equal(b, data) || last != int64(len(data)) {
		t.Fatalf("descarga incompleta: %d bytes, progreso %d", len(b), last)
	}

	// El APK no coincide con su hash: se rechaza y no queda en la carpeta.
	if _, err := c.Fetch(context.Background(), "0.20.0", "LanChat-0.20.0.apk", dir, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("hash distinto: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "LanChat-0.20.0.apk")); !os.IsNotExist(err) {
		t.Fatal("el archivo dañado no se borró")
	}
	// Sin hash publicado no se descarga.
	if _, err := c.Fetch(context.Background(), "0.20.0", "LanChat-0.20.0-x.zip", dir, nil); err == nil {
		t.Fatal("se aceptó un archivo sin hash")
	}
}

// fakePE arma un .exe mínimo que debug/pe acepta, de la máquina indicada.
func fakePE(machine uint16, content string) []byte {
	var b bytes.Buffer
	dos := make([]byte, 64)
	dos[0], dos[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(dos[0x3c:], 64)
	b.Write(dos)
	b.WriteString("PE\x00\x00")
	binary.Write(&b, binary.LittleEndian, pe.FileHeader{Machine: machine})
	b.WriteString(content)
	return b.Bytes()
}

func zipWith(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "LanChat-0.20.0.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	zw.Close()
	f.Close()
	return path
}

func TestInstallReplacesTheRightExe(t *testing.T) {
	exe64 := fakePE(pe.IMAGE_FILE_MACHINE_AMD64, "nuevo 64")
	exe32 := fakePE(pe.IMAGE_FILE_MACHINE_I386, "nuevo 32")
	zipPath := zipWith(t, map[string][]byte{"LanChat/LanChat.exe": exe64, "LanChat/LanChat_x86.exe": exe32, "LanChat/LEEME.txt": []byte("hola")})

	for goarch, want := range map[string][]byte{"amd64": exe64, "386": exe32} {
		dir := t.TempDir()
		exe := filepath.Join(dir, "lanchat.exe") // el nombre actual se conserva
		os.WriteFile(exe, []byte("viejo"), 0o755)
		if err := Install(zipPath, exe, goarch); err != nil {
			t.Fatal(goarch, err)
		}
		if b, _ := os.ReadFile(exe); !bytes.Equal(b, want) {
			t.Fatalf("%s: no se puso el ejecutable correcto", goarch)
		}
		if b, _ := os.ReadFile(exe + oldSuffix); string(b) != "viejo" {
			t.Fatalf("%s: el anterior no quedó como .old", goarch)
		}
		if err := Rollback(exe); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "viejo" {
			t.Fatal("Rollback no devolvió el ejecutable anterior")
		}
		RemoveOld(exe)
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("quedaron archivos de más: %v", entries)
		}
	}
}

func TestInstallRefusesWrongArchitecture(t *testing.T) {
	// Un zip con el de 64 bits en el lugar del de 32: no se instala.
	zipPath := zipWith(t, map[string][]byte{"LanChat/LanChat_x86.exe": fakePE(pe.IMAGE_FILE_MACHINE_AMD64, "")})
	exe := filepath.Join(t.TempDir(), "LanChat_x86.exe")
	os.WriteFile(exe, []byte("viejo"), 0o755)
	if err := Install(zipPath, exe, "386"); err == nil {
		t.Fatal("se instaló un ejecutable de otra arquitectura")
	}
	if b, _ := os.ReadFile(exe); string(b) != "viejo" {
		t.Fatal("el ejecutable actual cambió")
	}
	if _, err := os.Stat(exe + newSuffix); !os.IsNotExist(err) {
		t.Fatal("quedó el .new")
	}
	// Sin el ejecutable en el zip, tampoco.
	if err := Install(zipWith(t, map[string][]byte{"LanChat/LEEME.txt": nil}), exe, "amd64"); err == nil {
		t.Fatal("se aceptó un zip sin ejecutable")
	}
}
