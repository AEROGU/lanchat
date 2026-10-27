package discovery

import (
	"encoding/json"
	"errors"

	"github.com/AEROGU/lanchat/internal/protocol"
)

type packetType string

const (
	// typeHello: "acabo de llegar"; quien lo recibe responde con announce por unicast.
	typeHello packetType = "hello"
	// typeAnnounce: anuncio periódico de presencia.
	typeAnnounce packetType = "announce"
	// typeBye: el equipo se está cerrando.
	typeBye packetType = "bye"
)

type packet struct {
	Magic string `json:"m"`
	// Version es la versión del protocolo (protocol.Version).
	Version int `json:"v"`
	// App es la versión del programa del remitente (version.App).
	App      string     `json:"app,omitempty"`
	Type     packetType `json:"t"`
	ID       string     `json:"id"`
	Name     string     `json:"name,omitempty"`
	Hostname string     `json:"host"`
	// HTTPPort es el puerto donde el remitente atiende mensajes y archivos.
	HTTPPort int `json:"port"`
	// Status y StatusText: presencia (desde 0.10; las versiones viejas los ignoran).
	Status     string `json:"st,omitempty"`
	StatusText string `json:"stx,omitempty"`
	// Fingerprint es la huella de su identidad TLS. Solo sirve para guardarla la
	// primera vez y detectar si cambió: la conexión TLS es la que la comprueba.
	Fingerprint string `json:"fp"`
}

// decodePacket lee y valida un paquete. Uno de otra versión del protocolo se
// acepta si se entiende (para listar al equipo como incompatible): quien lo
// usa debe comprobar p.Version.
func decodePacket(b []byte) (packet, error) {
	var p packet
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Magic != protocol.Magic {
		return p, errors.New("paquete ajeno a lanchat")
	}
	if p.Version < 1 {
		return p, errors.New("versión de protocolo inválida")
	}
	switch p.Type {
	case typeHello, typeAnnounce, typeBye:
	default:
		return p, errors.New("tipo de paquete desconocido")
	}
	if p.HTTPPort < 1 || p.HTTPPort > 65535 {
		return p, errors.New("puerto inválido")
	}
	p.Status = protocol.NormalizeStatus(p.Status)
	return p, errors.Join(
		protocol.ValidateID(p.ID),
		protocol.ValidateName(p.Name),
		protocol.ValidateHostname(p.Hostname),
		protocol.ValidateAppVersion(p.App),
		protocol.ValidateStatusText(p.StatusText),
		protocol.ValidateFingerprint(p.Fingerprint),
	)
}
