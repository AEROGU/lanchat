package ui

import (
	"log/slog"

	"git.sr.ht/~jackmordaunt/go-toast/v2"
)

const (
	// appUserModelID es el nombre con el que Windows agrupa las notificaciones.
	appUserModelID = "LanChat"
	// activatorGUID identifica a LanChat ante Windows para recibir el clic en
	// una notificación. Es fijo: cambiarlo deja registros huérfanos.
	activatorGUID = "{6F1E8A2C-3B7D-4C59-9A0E-5D2F4B8C1E73}"
)

// notifier muestra notificaciones nativas de Windows. Registra la aplicación
// en HKCU\Software\Classes\AppUserModelId\LanChat (sin permisos de admin).
type notifier struct {
	log      *slog.Logger
	iconPath string
}

// newNotifier prepara las notificaciones; onClick se llama al hacer clic en una.
func newNotifier(iconPath string, onClick func(), log *slog.Logger) *notifier {
	if err := toast.SetAppData(toast.AppData{
		AppID:    appUserModelID,
		GUID:     activatorGUID,
		IconPath: iconPath,
	}); err != nil {
		log.Warn("registrando notificaciones", "err", err)
	}
	toast.SetActivationCallback(func(string, []toast.UserData) { onClick() })
	return &notifier{log: log, iconPath: iconPath}
}

func (n *notifier) notify(title, body string) {
	t := toast.Notification{
		AppID: appUserModelID,
		Title: title,
		Body:  body,
		Icon:  n.iconPath,
		Audio: toast.IM,
	}
	if err := t.Push(); err != nil {
		n.log.Warn("mostrando notificación", "err", err)
	}
}
