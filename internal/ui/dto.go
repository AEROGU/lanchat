package ui

import (
	"path/filepath"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/identity"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/thumb"
	"github.com/AEROGU/lanchat/internal/transfer"
	"github.com/AEROGU/lanchat/internal/version"
)

// Estructuras JSON que recibe la página. Las horas van en milisegundos Unix.

type stateJSON struct {
	Self        selfJSON      `json:"self"`
	Contacts    []contactJSON `json:"contacts"`
	Rooms       []roomJSON    `json:"rooms"`
	ManualPeers []string      `json:"manualPeers"`
	DownloadDir string        `json:"downloadDir"`
	// Mobile: sin escritorio (Android, ver Server.Shell): se eligen archivos
	// con <input type="file"> y no hay carpetas que abrir.
	Mobile bool       `json:"mobile"`
	Limits limitsJSON `json:"limits"`
}

type limitsJSON struct {
	MaxName         int `json:"maxName"`
	MaxMessageBytes int `json:"maxMessageBytes"`
	MaxStatusText   int `json:"maxStatusText"`
	// DeleteWindowMs: cuánto después de enviarlo se puede eliminar un mensaje para todos.
	DeleteWindowMs int64 `json:"deleteWindowMs"`
}

type selfJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	Version     string `json:"version"`
	AppName     string `json:"appName"`
	Copyright   string `json:"copyright"`
	LicenseName string `json:"licenseName"`
	Repository  string `json:"repository"`
	License     string `json:"license"`
	Status      string `json:"status"`
	StatusText  string `json:"statusText"`
	// Idle: ahora se anuncia Ausente por inactividad.
	Idle     bool `json:"idle"`
	AutoAway bool `json:"autoAway"`
	// ReadReceipts: se avisa a los demás cuando se leen sus mensajes.
	ReadReceipts bool `json:"readReceipts"`
	// Previews: se muestran miniaturas de las imágenes.
	Previews bool `json:"previews"`
	// HideOffline: la lista de contactos oculta a los desconectados.
	HideOffline bool `json:"hideOffline"`
	// CollapsedGroups son los grupos plegados en la lista de contactos.
	CollapsedGroups []string `json:"collapsedGroups"`
	// RecentEmoji son los últimos emojis usados, el más reciente primero.
	RecentEmoji []string `json:"recentEmoji"`
	Fingerprint string   `json:"fingerprint"`
}

func toSelfJSON(s app.Self) selfJSON {
	return selfJSON{
		ID: s.ID, Name: s.Name, Hostname: s.Hostname, Version: version.App,
		AppName: version.Name, Copyright: version.Copyright, License: version.License,
		LicenseName: version.LicenseName, Repository: version.Repository,
		Status: s.Status, StatusText: s.StatusText, Idle: s.Idle, AutoAway: s.AutoAwayEnabled,
		ReadReceipts: s.ReadReceipts, Previews: s.Previews, HideOffline: s.HideOffline,
		CollapsedGroups: nonNil(s.CollapsedGroups), RecentEmoji: nonNil(s.RecentEmoji),
		Fingerprint: identity.Format(s.Fingerprint),
	}
}

// nonNil hace que una lista vacía llegue al navegador como [] y no como null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type contactJSON struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Detail      string `json:"detail"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Group       string `json:"group"`
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	AppVersion  string `json:"appVersion"`
	// Incompatible: "older" si usa un protocolo anterior (debe actualizar),
	// "newer" si uno más nuevo (este equipo debe actualizar), "" si es
	// compatible o no se sabe. No se le envía nada.
	Incompatible string `json:"incompatible"`
	Online       bool   `json:"online"`
	LastSeen     int64  `json:"lastSeen"`
	Unread       int    `json:"unread"`
	// Fingerprint es la huella fijada; NewFingerprint, la que anuncia si cambió.
	Fingerprint     string `json:"fingerprint"`
	IdentityChanged bool   `json:"identityChanged"`
	NewFingerprint  string `json:"newFingerprint"`
	// Status: available, away o busy; vacío si está desconectado.
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	// Removed: quitado de la lista; solo sirve para nombrarlo en las salas.
	Removed bool `json:"removed"`
}

func toContactJSON(c app.Contact) contactJSON {
	out := contactJSON{
		ID:              c.ID,
		DisplayName:     c.DisplayName(),
		Detail:          c.Detail(),
		Name:            c.Name,
		Alias:           c.Alias,
		Group:           c.Group,
		Hostname:        c.Hostname,
		IP:              c.IP,
		AppVersion:      c.AppVersion,
		Online:          c.Online,
		LastSeen:        c.LastSeen.UnixMilli(),
		Unread:          c.Unread,
		Status:          c.Status,
		StatusText:      c.StatusText,
		Fingerprint:     identity.Format(c.Fingerprint),
		IdentityChanged: c.IdentityChanged(),
		Removed:         c.Removed,
	}
	switch {
	case !c.Incompatible():
	case c.Protocol < protocol.Version:
		out.Incompatible = "older"
	default:
		out.Incompatible = "newer"
	}
	if out.IdentityChanged {
		out.NewFingerprint = identity.Format(c.AnnouncedFingerprint)
	}
	return out
}

type messageJSON struct {
	ID       string `json:"id"`
	PeerID   string `json:"peerId"`
	Outgoing bool   `json:"outgoing"`
	Body     string `json:"body"`
	At       int64  `json:"at"`
	// Status: "pending" (saliente sin confirmar) o "delivered".
	Status string `json:"status"`
	// RoomID: sala del mensaje ("" = conversación con PeerID). En una sala,
	// PeerID es el autor.
	RoomID string `json:"roomId"`
	// Kind: "text", "files" o "event" (aviso de sala); en "files", Transfer trae la oferta.
	Kind     string        `json:"kind"`
	Transfer *transferJSON `json:"transfer,omitempty"`
	// Broadcast: enviado a varios contactos a la vez.
	Broadcast bool `json:"broadcast"`
	// ReadAt: cuándo el destinatario lo leyó (0 si no se sabe).
	ReadAt int64 `json:"readAt"`
	// Deleted: su autor lo eliminó para todos (Body viene vacío).
	Deleted bool `json:"deleted"`
	// Reactions: ausente si el evento no las trae (la página conserva las que
	// tenía); una lista, aunque esté vacía, las reemplaza.
	Reactions *[]reactionJSON `json:"reactions,omitempty"`
	// MergedFrom: era de otro contacto y se combinó en esta conversación.
	MergedFrom string `json:"mergedFrom,omitempty"`
	// Edited: su autor cambió el texto después de enviarlo.
	Edited bool `json:"edited"`
	// Forwarded: es un reenvío de otro mensaje.
	Forwarded bool `json:"forwarded"`
	// Reply: la cita del mensaje al que responde (ausente si no responde a nada).
	Reply *replyJSON `json:"reply,omitempty"`
}

// replyJSON es la cita de una respuesta (ver store.Reply).
type replyJSON struct {
	ID string `json:"id"`
	// Author es el ID de quien escribió el mensaje citado.
	Author string `json:"author"`
	Text   string `json:"text"`
	// Deleted: el mensaje citado se eliminó para todos (Text viene vacío).
	Deleted bool `json:"deleted"`
}

// reactionJSON es la reacción de un equipo a un mensaje.
type reactionJSON struct {
	// Peer es quien reaccionó; "" si fue este equipo.
	Peer  string `json:"peer"`
	Emoji string `json:"emoji"`
}

func toMessageJSON(m store.Message) messageJSON {
	status := "delivered"
	if m.Status == store.StatusPending {
		status = "pending"
	}
	kind := "text"
	switch m.Kind {
	case store.KindFiles:
		kind = "files"
	case store.KindRoomEvent:
		kind = "event"
	}
	var reactions *[]reactionJSON
	if m.Reactions != nil {
		list := make([]reactionJSON, len(m.Reactions))
		for i, r := range m.Reactions {
			list[i] = reactionJSON{Peer: r.PeerID, Emoji: r.Emoji}
		}
		reactions = &list
	}
	return messageJSON{
		Deleted:    m.Deleted,
		Reactions:  reactions,
		Kind:       kind,
		ID:         m.ID,
		PeerID:     m.PeerID,
		RoomID:     m.RoomID,
		Outgoing:   m.Outgoing,
		Body:       m.Body,
		At:         m.At.UnixMilli(),
		Status:     status,
		Broadcast:  m.Broadcast,
		ReadAt:     unixMilli(m.ReadAt),
		MergedFrom: m.MergedFrom,
		Edited:     !m.EditedAt.IsZero() && !m.Deleted,
		Forwarded:  m.Forwarded,
		Reply:      toReplyJSON(m),
	}
}

func toReplyJSON(m store.Message) *replyJSON {
	if m.Reply.ID == "" || m.Deleted {
		return nil
	}
	r := m.Reply
	return &replyJSON{ID: r.ID, Author: r.Author, Text: r.Text, Deleted: r.Text == ""}
}

// unixMilli es 0 para la hora cero (en vez de un número negativo).
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

type transferJSON struct {
	ID       string `json:"id"`
	PeerID   string `json:"peerId"`
	Outgoing bool   `json:"outgoing"`
	// State: offered, downloading, completed, rejected, canceled, expired o failed.
	State     string     `json:"state"`
	Error     string     `json:"error"`
	ExpiresAt int64      `json:"expiresAt"`
	Total     int64      `json:"total"`
	Dir       string     `json:"dir"`
	Files     []fileJSON `json:"files"`
}

type fileJSON struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Done  bool   `json:"done"`
	// Dir es la subcarpeta ("Proyecto/planos"; "" = suelto).
	Dir string `json:"dir"`
	// SavedName es el nombre con el que quedó guardado (puede ser "x (1).pdf").
	SavedName string `json:"savedName,omitempty"`
	// Thumb: hay miniatura (GET /api/files/thumb) para la vista previa.
	Thumb bool `json:"thumb,omitempty"`
	// View: se puede abrir la imagen completa (GET /api/files/view): una
	// imagen enviada, o recibida y terminada.
	View bool `json:"view,omitempty"`
}

func toTransferJSON(t store.Transfer) transferJSON {
	out := transferJSON{
		ID:        t.ID,
		PeerID:    t.PeerID,
		Outgoing:  t.Outgoing,
		State:     t.State.String(),
		Error:     t.Error,
		ExpiresAt: t.ExpiresAt.UnixMilli(),
		Total:     t.TotalSize(),
		Dir:       t.Dir,
		Files:     make([]fileJSON, len(t.Files)),
	}
	for i, f := range t.Files {
		out.Files[i] = fileJSON{Index: f.Index, Name: f.Name, Size: f.Size, Done: f.Done, Dir: f.Dir,
			Thumb: f.HasThumb, View: thumb.Supported(f.Name) && (t.Outgoing || f.Done)}
		if !t.Outgoing && f.Done {
			out.Files[i].SavedName = filepath.Base(f.Path)
		}
	}
	return out
}

type progressJSON struct {
	ID    string  `json:"id"`
	Done  int64   `json:"done"`
	Total int64   `json:"total"`
	Rate  float64 `json:"rate"` // bytes por segundo
}

func toProgressJSON(p transfer.Progress) progressJSON {
	return progressJSON{ID: p.ID, Done: p.Done, Total: p.Total, Rate: p.Rate}
}
