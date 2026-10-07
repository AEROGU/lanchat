// Package testutil tiene ayudas para las pruebas.
package testutil

import (
	"os"
	"testing"
	"time"
)

// removeTimeout: cuánto se reintenta borrar la carpeta temporal.
const removeTimeout = 5 * time.Second

// TempDir es como t.TempDir pero reintenta el borrado: en Windows, el
// antivirus puede retener un instante los archivos que SQLite acaba de
// borrar (-wal, -shm) y la carpeta aparece "no vacía".
func TempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lanchat-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(removeTimeout)
		for err := os.RemoveAll(dir); err != nil; err = os.RemoveAll(dir) {
			if time.Now().After(deadline) {
				t.Errorf("borrando %s: %v", dir, err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}
