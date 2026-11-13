//go:build mage

package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"

	"github.com/AEROGU/lanchat/internal/icon"
)

const (
	productName = "LanChat"
	// langSpanishMX es el idioma de los textos de versión (es-MX).
	langSpanishMX = 0x080A
	readmeSource  = "packaging/LEEME.txt"
	readmeName    = "LEEME.txt"
	// uninstallScript abre el desinstalador con doble clic.
	uninstallScript = "packaging/Desinstalar LanChat.cmd"
	// checksumsName lista el SHA-256 del zip (formato de sha256sum).
	checksumsName = "SHA256SUMS.txt"
)

// Resources genera el ícono, los datos de versión y el manifest del .exe, uno
// por arquitectura: cmd/lanchat/rsrc_windows_<arq>.syso, que go build incluye
// solo al compilar para esa arquitectura.
func Resources() error {
	v := appVersion()
	images := make([]image.Image, len(icon.ExeSizes))
	for i, s := range icon.ExeSizes {
		images[i] = icon.Draw(s, false)
	}
	ico, err := winres.NewIconFromImages(images)
	if err != nil {
		return err
	}
	rs := &winres.ResourceSet{}
	if err := rs.SetIcon(winres.Name("APP"), ico); err != nil {
		return err
	}
	rs.SetManifest(winres.AppManifest{
		Description:    "LanChat: mensajería en la red local",
		ExecutionLevel: winres.AsInvoker,
		// Nítido en pantallas con escala y diálogos de Windows con estilo actual.
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		LongPathAware:       true,
	})

	vi := version.Info{FileVersion: numericVersion(v), ProductVersion: numericVersion(v)}
	for key, value := range map[string]string{
		version.ProductName:      productName,
		version.FileDescription:  "LanChat: mensajería en la red local",
		version.ProductVersion:   v,
		version.FileVersion:      v,
		version.OriginalFilename: exeName,
		version.InternalName:     "lanchat",
	} {
		if err := vi.Set(langSpanishMX, key, value); err != nil {
			return err
		}
	}
	rs.SetVersionInfo(vi)

	for _, a := range winArches {
		if err := writeResources(rs, a.goarch); err != nil {
			return err
		}
	}
	return nil
}

func writeResources(rs *winres.ResourceSet, goarch string) error {
	f, err := os.Create(fmt.Sprintf("cmd/lanchat/rsrc_windows_%s.syso", goarch))
	if err != nil {
		return err
	}
	if err := rs.WriteObject(f, winres.Arch(goarch)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g[0-9a-f]+)?`)

// numericVersion convierte "1.2.0" o "1.2.0-3-gabc1234" en 1.2.0.0 / 1.2.0.3,
// el formato que pide Windows. Sin etiqueta de versión queda 0.0.0.0.
func numericVersion(v string) [4]uint16 {
	var out [4]uint16
	m := versionRe.FindStringSubmatch(v)
	for i := 1; m != nil && i < len(m); i++ {
		n, _ := strconv.ParseUint(m[i], 10, 16)
		out[i-1] = uint16(n)
	}
	return out
}

// Dist arma dist/LanChat-<versión>.zip (64 bits) y
// dist/LanChat-<versión>-32bits.zip, cada uno con su ejecutable (como
// lanchat.exe), LEEME.txt, el acceso al desinstalador, la licencia y los
// avisos de terceros, y dist/SHA256SUMS.txt con el hash de ambos.
func Dist() error {
	mg.Deps(Build, Build32, Notices)
	v := appVersion()
	readme, err := os.ReadFile(readmeSource)
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(readme), "{{VERSION}}", v)
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n") // Bloc de notas

	var zips []string
	for _, a := range winArches {
		out := filepath.Join(distDir, fmt.Sprintf("%s-%s%s.zip", productName, v, a.zipSuffix))
		if err := writeZip(out, a.exe, text); err != nil {
			return err
		}
		fmt.Println(out)
		zips = append(zips, out)
	}
	return writeChecksums(zips...)
}

// writeZip arma un zip de LanChat con el ejecutable dist/<exe>.
func writeZip(out, exe, readme string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = errors.Join(
		addFile(zw, productName+"/"+exeName, filepath.Join(distDir, exe)),
		addFile(zw, productName+"/LICENSE.txt", "LICENSE"),
		addFile(zw, productName+"/"+filepath.Base(uninstallScript), uninstallScript),
		addFile(zw, productName+"/"+noticesName, filepath.Join(distDir, noticesName)),
	)
	if err == nil {
		var w io.Writer
		hdr := &zip.FileHeader{Name: productName + "/" + readmeName, Method: zip.Deflate, Modified: time.Now()}
		if w, err = zw.CreateHeader(hdr); err == nil {
			_, err = io.WriteString(w, readme)
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeChecksums escribe dist/SHA256SUMS.txt; se verifica con
// "sha256sum -c SHA256SUMS.txt" o con Get-FileHash en PowerShell.
func writeChecksums(paths ...string) error {
	var lines strings.Builder
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(&lines, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(path))
	}
	out := filepath.Join(distDir, checksumsName)
	fmt.Println(out)
	return os.WriteFile(out, []byte(lines.String()), 0o644)
}

func addFile(zw *zip.Writer, name, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(fi)
	if err != nil {
		return err
	}
	hdr.Name, hdr.Method = name, zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}
