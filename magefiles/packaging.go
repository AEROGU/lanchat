//go:build mage

package main

import (
	"archive/zip"
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
	// resourceFile lo incluye go build automáticamente en el .exe de Windows.
	resourceFile = "cmd/lanchat/rsrc_windows_amd64.syso"
	productName  = "LanChat"
	// langSpanishMX es el idioma de los textos de versión (es-MX).
	langSpanishMX = 0x080A
	readmeSource  = "packaging/LEEME.txt"
	readmeName    = "LEEME.txt"
)

// Resources genera el ícono, los datos de versión y el manifest del .exe.
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

	f, err := os.Create(resourceFile)
	if err != nil {
		return err
	}
	if err := rs.WriteObject(f, winres.ArchAMD64); err != nil {
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

// Dist arma dist/LanChat-<versión>.zip con el ejecutable y LEEME.txt.
func Dist() error {
	mg.Deps(Build)
	v := appVersion()
	readme, err := os.ReadFile(readmeSource)
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(readme), "{{VERSION}}", v)
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n") // Bloc de notas

	out := filepath.Join(distDir, fmt.Sprintf("%s-%s.zip", productName, v))
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = addFile(zw, productName+"/"+exeName, filepath.Join(distDir, exeName))
	if err == nil {
		var w io.Writer
		hdr := &zip.FileHeader{Name: productName + "/" + readmeName, Method: zip.Deflate, Modified: time.Now()}
		if w, err = zw.CreateHeader(hdr); err == nil {
			_, err = io.WriteString(w, text)
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
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
