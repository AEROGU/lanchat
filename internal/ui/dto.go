package ui

import (
	"path/filepath"
	"time"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/identity"
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
	ReadReceipts bool   `json:"readReceipts"`
	Fingerprint  string `json:"fingerprint"`
}

func toSelfJSON(s app.Self) selfJSON {
	return selfJSON{
		ID: s.ID, Name: s.Name, Hostname: s.Hostname, Version: version.App,
		AppName: version.Name, Copyright: version.Copyright, License: version.License,
		LicenseName: version.LicenseName, Repository: version.Repository,
		Status: s.Status, StatusText: s.StatusText, Idle: s.Idle, AutoAway: s.AutoAwayEnabled,
		ReadReceipts: s.ReadReceipts,
		Fingerprint:  identity.Format(s.Fingerprint),
	}
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
	Online      bool   `json:"online"`
	LastSeen    int64  `json:"lastSeen"`
	Unread      int    `json:"unread"`
	// Fingerprint es la huella fijada; NewFingerprint, la que anuncia si cambió.
	Fingerprint     string `json:"fingerprint"`
	IdentityChanged bool   `json:"identityChanged"`
	NewFingerprint  string `json:"newFingerprint"`
	// Status: available, away o busy; vacío si está desconectado.
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
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
	return messageJSON{
		Kind:      kind,
		ID:        m.ID,
		PeerID:    m.PeerID,
		RoomID:    m.RoomID,
		Outgoing:  m.Outgoing,
		Body:      m.Body,
		At:        m.At.UnixMilli(),
		Status:    status,
		Broadcast: m.Broadcast,
		ReadAt:    unixMilli(m.ReadAt),
	}
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
