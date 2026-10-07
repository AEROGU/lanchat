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
