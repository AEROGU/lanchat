package ui

import (
	"path/filepath"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/transfer"
	"github.com/AEROGU/lanchat/internal/version"
)

// Estructuras JSON que recibe la página. Las horas van en milisegundos Unix.

type stateJSON struct {
	Self        selfJSON      `json:"self"`
	Contacts    []contactJSON `json:"contacts"`
	ManualPeers []string      `json:"manualPeers"`
	DownloadDir string        `json:"downloadDir"`
	Limits      limitsJSON    `json:"limits"`
}

type limitsJSON struct {
	MaxName         int `json:"maxName"`
	MaxMessageBytes int `json:"maxMessageBytes"`
	MaxStatusText   int `json:"maxStatusText"`
}

type selfJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	Version    string `json:"version"`
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	// Idle: ahora se anuncia Ausente por inactividad.
	Idle     bool `json:"idle"`
	AutoAway bool `json:"autoAway"`
}

func toSelfJSON(s app.Self) selfJSON {
	return selfJSON{
		ID: s.ID, Name: s.Name, Hostname: s.Hostname, Version: version.App,
		Status: s.Status, StatusText: s.StatusText, Idle: s.Idle, AutoAway: s.AutoAwayEnabled,
	}
}

type contactJSON struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Detail      string `json:"detail"`
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	AppVersion  string `json:"appVersion"`
	Online      bool   `json:"online"`
	LastSeen    int64  `json:"lastSeen"`
	Unread      int    `json:"unread"`
	// Status: available, away o busy; vacío si está desconectado.
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
}

func toContactJSON(c app.Contact) contactJSON {
	return contactJSON{
		ID:          c.ID,
		DisplayName: c.DisplayName(),
		Detail:      c.Detail(),
		Name:        c.Name,
		Alias:       c.Alias,
		Hostname:    c.Hostname,
		IP:          c.IP,
		AppVersion:  c.AppVersion,
		Online:      c.Online,
		LastSeen:    c.LastSeen.UnixMilli(),
		Unread:      c.Unread,
		Status:      c.Status,
		StatusText:  c.StatusText,
	}
}

type messageJSON struct {
	ID       string `json:"id"`
	PeerID   string `json:"peerId"`
	Outgoing bool   `json:"outgoing"`
	Body     string `json:"body"`
	At       int64  `json:"at"`
	// Status: "pending" (saliente sin confirmar) o "delivered".
	Status string `json:"status"`
	// Kind: "text" o "files"; en "files", Transfer trae la oferta.
	Kind     string        `json:"kind"`
	Transfer *transferJSON `json:"transfer,omitempty"`
}

func toMessageJSON(m store.Message) messageJSON {
	status := "delivered"
	if m.Status == store.StatusPending {
		status = "pending"
	}
	kind := "text"
	if m.Kind == store.KindFiles {
		kind = "files"
	}
	return messageJSON{
		Kind:     kind,
		ID:       m.ID,
		PeerID:   m.PeerID,
		Outgoing: m.Outgoing,
		Body:     m.Body,
		At:       m.At.UnixMilli(),
		Status:   status,
	}
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
	// SavedName es el nombre con el que quedó guardado (puede ser "x (1).pdf").
	SavedName string `json:"savedName,omitempty"`
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
		out.Files[i] = fileJSON{Index: f.Index, Name: f.Name, Size: f.Size, Done: f.Done}
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
