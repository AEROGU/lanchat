package protocol

import "github.com/AEROGU/lanchat/internal/i18n"

// Tipos de aviso de sala (RoomNotice.Type).
const (
	NoticeCreated = "created" // creó la sala Name
	NoticeAdded   = "added"   // agregó a Members
	NoticeRenamed = "renamed" // le puso Name a la sala
	NoticeRemoved = "removed" // sacó a Members[0]
	NoticeAdmin   = "admin"   // hizo administrador a Members[0]
	NoticeUnadmin = "unadmin" // le quitó el privilegio de administrador a Members[0]
	NoticeLeft    = "left"    // salió de la sala
)

// RoomNotice describe un aviso de sala ("Ana agregó a Luis") para que cada
// equipo lo muestre en su idioma; quien lo hizo es el autor del mensaje.
// Viaja junto al texto del aviso, que siguen mostrando las versiones que no
// lo conocen (hasta la 0.22, en español).
type RoomNotice struct {
	Type string `json:"type"`
	// Name es el nombre de la sala (created, renamed).
	Name string `json:"name,omitempty"`
	// Members son los afectados, con el nombre que les veía el autor por si
	// quien recibe no los conoce.
	Members []NoticeMember `json:"members,omitempty"`
}

// NoticeMember es un miembro nombrado en un aviso de sala.
type NoticeMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// KnownNotice indica si esta versión sabe mostrar avisos del tipo t (uno de
// una versión futura se muestra con su texto).
func KnownNotice(t string) bool {
	switch t {
	case NoticeCreated, NoticeAdded, NoticeRenamed, NoticeRemoved, NoticeAdmin, NoticeUnadmin, NoticeLeft:
		return true
	}
	return false
}

// ValidateRoomNotice comprueba un aviso de un tipo conocido.
func ValidateRoomNotice(n RoomNotice) error {
	if !KnownNotice(n.Type) || len(n.Members) > MaxRoomMembers || len(n.Name) > 4*MaxNameLen {
		return i18n.E("protocol.badNotice")
	}
	for _, m := range n.Members {
		if ValidateID(m.ID) != nil || len(m.Name) > 4*MaxNameLen {
			return i18n.E("protocol.badNotice")
		}
	}
	return nil
}
