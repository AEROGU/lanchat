// Package identity es la identidad criptográfica de cada instalación de
// LanChat: una clave ECDSA P-256 con un certificado propio (no lo firma
// ninguna autoridad). Los demás equipos la reconocen por su huella, el
// SHA-256 de la clave pública, y la guardan la primera vez que la ven.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	keyFile  = "identity.key"
	certFile = "identity.crt"
	// certValidity: el certificado no se renueva; lo que importa es la clave.
	certValidity = 30 * 365 * 24 * time.Hour
)

type Identity struct {
	Cert tls.Certificate
	// Fingerprint es la huella (SHA-256 de la clave pública, en hex).
	Fingerprint string
}

// Load lee la identidad de dir o la crea la primera vez. id es el ID de la
// instalación, que solo se usa como nombre del certificado.
func Load(dir, id string) (*Identity, error) {
	keyPath, certPath := filepath.Join(dir, keyFile), filepath.Join(dir, certFile)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		if err := generate(keyPath, certPath, id); err != nil {
			return nil, fmt.Errorf("creando la identidad: %w", err)
		}
		cert, err = tls.LoadX509KeyPair(certPath, keyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("leyendo la identidad (%s): %w", keyPath, err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	cert.Leaf = leaf
	return &Identity{Cert: cert, Fingerprint: Fingerprint(leaf)}, nil
}

func generate(keyPath, certPath, id string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "LanChat " + id},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	// La clave primero: sin ella el certificado no sirve.
	if err := writePEM(keyPath, "PRIVATE KEY", keyDER, 0o600); err != nil {
		return err
	}
	return writePEM(certPath, "CERTIFICATE", der, 0o644)
}

func writePEM(path, typ string, der []byte, perm os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), perm)
}

// Fingerprint es el SHA-256 (hex) de la clave pública del certificado: no
// cambia aunque se renovara el certificado con la misma clave.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// Format muestra una huella en grupos de 4 para compararla de palabra:
// "3F2A 9C11 …".
func Format(fp string) string {
	var groups []string
	for i := 0; i < len(fp); i += 4 {
		groups = append(groups, strings.ToUpper(fp[i:min(i+4, len(fp))]))
	}
	return strings.Join(groups, " ")
}
