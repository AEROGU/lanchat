// Package update busca la última versión publicada de LanChat en GitHub, la
// descarga y comprueba su SHA-256 con el SHA256SUMS.txt de la misma Release.
//
// No usa la API de GitHub (60 consultas por hora por IP, poco para una
// oficina detrás de una sola IP): la última versión sale de la redirección de
// /releases/latest a /releases/tag/vX.Y.Z, y los archivos de
// /releases/download/vX.Y.Z/<archivo>. GitHub nunca marca como "latest" una
// versión preliminar.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ReleasesURL es la página de versiones del proyecto. Es una variable para
// probar con un servidor propio (-ldflags "-X …/internal/update.ReleasesURL=…").
var ReleasesURL = "https://github.com/AEROGU/lanchat/releases"

const (
	// ChecksumsName es el archivo de la Release con el SHA-256 de cada archivo.
	ChecksumsName = "SHA256SUMS.txt"
	// maxDownload acota lo que se descarga (el zip pesa unos 20 MB).
	maxDownload = 300 << 20
	// maxChecksums acota SHA256SUMS.txt.
	maxChecksums = 64 << 10
	// requestTimeout limita cada consulta corta (la última versión, los hashes).
	requestTimeout = 30 * time.Second
	// downloadTimeout limita la descarga completa, en redes lentas.
	downloadTimeout = 30 * time.Minute
)

// ErrNoRelease: el proyecto todavía no publicó ninguna versión.
var ErrNoRelease = errors.New("todavía no hay versiones publicadas")

// Client consulta y descarga las Releases. El valor cero sirve.
type Client struct {
	// HTTP es el cliente para las peticiones; nil = uno con tiempos límite.
	HTTP *http.Client
	// Base reemplaza a ReleasesURL (pruebas).
	Base string
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimSuffix(c.Base, "/")
	}
	return strings.TrimSuffix(ReleasesURL, "/")
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

var defaultHTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: requestTimeout,
	IdleConnTimeout:       time.Minute,
}}

// Latest devuelve la última versión publicada (sin la v), p. ej. "0.20.0".
func (c *Client) Latest(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/latest", nil)
	if err != nil {
		return "", err
	}
	hc := *c.http()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return "", connError(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", fmt.Errorf("la página de versiones respondió %s", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("la página de versiones no indicó la última: %w", err)
	}
	// …/releases/tag/v0.20.0; sin versiones, GitHub manda a …/releases.
	dir, tag := path.Split(loc.Path)
	if !strings.HasSuffix(dir, "/tag/") {
		return "", ErrNoRelease
	}
	tag, _ = url.PathUnescape(tag)
	v, ok := Parse(tag)
	if !ok || !v.Release() {
		return "", fmt.Errorf("la última versión publicada tiene un nombre inesperado (%q)", tag)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// AssetURL es la dirección de un archivo de la Release de esa versión.
func (c *Client) AssetURL(version, name string) string {
	return c.base() + "/download/v" + url.PathEscape(version) + "/" + url.PathEscape(name)
}

// Fetch descarga el archivo name de la Release de version en la carpeta dir
// y comprueba su SHA-256 con SHA256SUMS.txt; si no coincide, lo borra.
// progress, si no es nil, recibe lo descargado y el total (-1 si no se sabe).
func (c *Client) Fetch(ctx context.Context, version, name, dir string, progress func(done, total int64)) (string, error) {
	sums, err := c.checksums(ctx, version)
	if err != nil {
		return "", err
	}
	want, ok := sums[name]
	if !ok {
		return "", fmt.Errorf("la versión %s no publica el SHA-256 de %s", version, name)
	}

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	resp, err := c.get(ctx, c.AssetURL(version, name))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxDownload {
		return "", fmt.Errorf("%s es demasiado grande (%d bytes)", name, resp.ContentLength)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: progress}
	_, err = io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(resp.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = fmt.Errorf("la descarga de %s se interrumpió: %w", name, err)
	case pw.done > maxDownload:
		err = fmt.Errorf("%s es demasiado grande", name)
	case hex.EncodeToString(h.Sum(nil)) != want:
		err = fmt.Errorf("%s llegó dañado: su SHA-256 no coincide con el publicado; vuelve a intentarlo", name)
	}
	if err != nil {
		os.Remove(dst)
		return "", err
	}
	return dst, nil
}

// checksums lee SHA256SUMS.txt (formato de sha256sum: "hash  archivo").
func (c *Client) checksums(ctx context.Context, version string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.get(ctx, c.AssetURL(version, ChecksumsName))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxChecksums))
	for sc.Scan() {
		hash, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		name = strings.TrimPrefix(strings.TrimSpace(name), "*") // "*" = modo binario
		if _, err := hex.DecodeString(hash); !ok || err != nil || len(hash) != 2*sha256.Size || name == "" {
			continue
		}
		sums[name] = strings.ToLower(hash)
	}
	if err := sc.Err(); err != nil {
		return nil, connError(err)
	}
	return sums, nil
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, connError(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("no se pudo descargar %s (%s)", path.Base(req.URL.Path), resp.Status)
	}
	return resp, nil
}

// connError explica un fallo de red en palabras del usuario.
func connError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("no se pudo conectar con GitHub (¿hay internet en este equipo?): %w", err)
}

type progressWriter struct {
	done, total int64
	fn          func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.fn != nil {
		total := p.total
		if total <= 0 {
			total = -1
		}
		p.fn(p.done, total)
	}
	return len(b), nil
}
