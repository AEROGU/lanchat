//go:build mage

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const screenshotsDir = "docs/screenshots"

// scene es una captura del README: archivo, fragmento de la dirección que
// abre la escena (ver internal/ui/demo_test.go) y si va en tema oscuro.
type scene struct {
	name, fragment string
	dark           bool
}

var scenes = []scene{
	{"conversacion", "chat=Carlos%20Ruiz", false},
	{"sala", "room=Cierre%20de%20mes", false},
	{"mensaje-a-varios", "many=Contabilidad", false},
	{"tema-oscuro", "chat=Carlos%20Ruiz", true},
}

// Screenshots regenera docs/screenshots/*.png y docs/logo.png con la oficina
// de demostración (datos ficticios) y Edge sin ventana.
func Screenshots() error {
	edge, err := findEdge()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "lanchat-screenshots-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	demo := filepath.Join(tmp, "demo.exe")
	if err := exec.Command("go", "test", "-c", "-tags", "demo", "-o", demo, "./internal/ui").Run(); err != nil {
		return fmt.Errorf("compilando la demostración: %w", err)
	}
	logo := exec.Command(demo, "-test.run", "^TestDemoLogo$")
	logo.Env = append(os.Environ(), "LANCHAT_DEMO_LOGO="+filepath.Join("docs", "logo.png"))
	if out, err := logo.CombinedOutput(); err != nil {
		return fmt.Errorf("logotipo: %w\n%s", err, out)
	}

	urlFile := filepath.Join(tmp, "url.txt")
	server := exec.Command(demo, "-test.run", "^TestDemo$", "-test.timeout", "0")
	server.Env = append(os.Environ(), "LANCHAT_DEMO_URL="+urlFile)
	if err := server.Start(); err != nil {
		return err
	}
	defer func() {
		server.Process.Kill()
		server.Wait()
	}()
	url, err := waitFile(urlFile, 10*time.Second)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(screenshotsDir, 0o755); err != nil {
		return err
	}
	for _, s := range scenes {
		out, err := filepath.Abs(filepath.Join(screenshotsDir, s.name+".png"))
		if err != nil {
			return err
		}
		scheme := "1" // claro
		if s.dark {
			scheme = "0"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		// Un perfil nuevo por captura: si no, Edge se une al proceso anterior.
		err = exec.CommandContext(ctx, edge, "--headless=new", "--disable-gpu", "--hide-scrollbars",
			"--no-first-run", "--user-data-dir="+filepath.Join(tmp, "edge-"+s.name),
			"--window-size=1100,680", "--force-device-scale-factor=1",
			"--blink-settings=preferredColorScheme="+scheme, "--virtual-time-budget=8000",
			"--screenshot="+out, url+"#"+s.fragment).Run()
		cancel()
		if err != nil {
			return fmt.Errorf("captura %s: %w", s.name, err)
		}
		fmt.Println(out)
	}
	return nil
}

func findEdge() (string, error) {
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		p := filepath.Join(os.Getenv(env), "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no se encontró Microsoft Edge")
}

// waitFile espera a que path exista y tenga contenido.
func waitFile(path string, timeout time.Duration) (string, error) {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b)), nil
		}
	}
	return "", fmt.Errorf("la demostración no escribió %s", path)
}
