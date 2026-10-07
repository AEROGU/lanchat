package ui

import (
	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/version"
)

// Estructuras JSON que recibe la página. Las horas van en milisegundos Unix.

type stateJSON struct {
	Self        selfJSON      `json:"self"`
	Contacts    []contactJSON `json:"contacts"`
	ManualPeers []string      `json:"manualPeers"`
	Limits      limitsJSON    `json:"limits"`
}

type limitsJSON struct {
	MaxName         int `json:"maxName"`
	MaxMessageBytes int `json:"maxMessageBytes"`
}

type selfJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
}

func toSelfJSON(s app.Self) selfJSON {
	return selfJSON{ID: s.ID, Name: s.Name, Hostname: s.Hostname, Version: version.App}
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
}

func toMessageJSON(m store.Message) messageJSON {
	status := "delivered"
	if m.Status == store.StatusPending {
		status = "pending"
	}
	return messageJSON{
		ID:       m.ID,
		PeerID:   m.PeerID,
		Outgoing: m.Outgoing,
		Body:     m.Body,
		At:       m.At.UnixMilli(),
		Status:   status,
	}
}
