package discovery

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const (
	protoMagic   = "lanchat"
	protoVersion = 1

	maxIDLen   = 64
	maxNameLen = 64
	maxHostLen = 255
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
	Magic    string     `json:"m"`
	Version  int        `json:"v"`
	Type     packetType `json:"t"`
	ID       string     `json:"id"`
	Name     string     `json:"name,omitempty"`
	Hostname string     `json:"host"`
	// Port es el puerto HTTP del equipo (mensajes y archivos).
	Port int `json:"port"`
}

func decodePacket(b []byte) (packet, error) {
	var p packet
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Magic != protoMagic || p.Version < 1 {
		return p, errors.New("paquete ajeno a lanchat")
	}
	switch p.Type {
	case typeHello, typeAnnounce, typeBye:
	default:
		return p, errors.New("tipo de paquete desconocido")
	}
	if p.ID == "" || len(p.ID) > maxIDLen {
		return p, errors.New("id inválido")
	}
	if utf8.RuneCountInString(p.Name) > maxNameLen || len(p.Hostname) > maxHostLen {
		return p, errors.New("nombre demasiado largo")
	}
	if p.Port < 1 || p.Port > 65535 {
		return p, errors.New("puerto inválido")
	}
	return p, nil
}
