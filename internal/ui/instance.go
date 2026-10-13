package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Una sola instancia por usuario: la que está abierta guarda en ui.json su
// puerto y token; una segunda instancia los lee, le pide que muestre su
// ventana y termina.

const (
	instanceFile    = "ui.json"
	instanceTimeout = 2 * time.Second
)

type instanceInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func writeInstance(dir string, port int, token string) error {
	b, err := json.Marshal(instanceInfo{port, token})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, instanceFile), b, 0o600)
}

func removeInstance(dir string) {
	os.Remove(filepath.Join(dir, instanceFile))
}

// signalRunning pide a la instancia abierta que muestre su ventana. Devuelve
// false si no hay ninguna (o no responde).
func signalRunning(dir string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, instanceFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var info instanceInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return false, nil // archivo dañado: se sobrescribirá
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/api/open", info.Port), strings.NewReader("{}"))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.Token)
	resp, err := (&http.Client{Timeout: instanceTimeout}).Do(req)
	if err != nil {
		return false, nil // quedó de una ejecución anterior que no cerró bien
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent, nil
}

// QuitRunning pide a la instancia abierta con esa carpeta de datos que se
// cierre (también su ventana) y espera hasta timeout. wasRunning indica si
// había una; el error, que no terminó de cerrarse a tiempo.
func QuitRunning(dir string, timeout time.Duration) (wasRunning bool, err error) {
	path := filepath.Join(dir, instanceFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var info instanceInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return false, nil
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/api/quit", info.Port), strings.NewReader("{}"))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.Token)
	resp, err := (&http.Client{Timeout: instanceTimeout}).Do(req)
	if err != nil {
		return false, nil // el archivo quedó de una ejecución que no cerró bien
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return true, fmt.Errorf("LanChat no aceptó cerrarse (respondió %d)", resp.StatusCode)
	}
	// Al terminar, la instancia borra su archivo.
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
	}
	return true, errors.New("LanChat sigue abierto: ciérralo desde el icono de la bandeja (clic derecho > Salir)")
}
