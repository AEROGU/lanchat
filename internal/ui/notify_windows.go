package ui

import (
	"errors"
	"log/slog"

	"git.sr.ht/~jackmordaunt/go-toast/v2"
	"golang.org/x/sys/windows/registry"
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
	forgetStaleIcon(iconPath)
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

// UnregisterNotifications borra el registro de LanChat como emisor de
// notificaciones (HKCU\Software\Classes\AppUserModelId\LanChat).
func UnregisterNotifications() error {
	err := registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appUserModelID)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// forgetStaleIcon borra del registro el ícono de las notificaciones si apunta
// a otra ruta: go-toast no reemplaza un valor que ya existe, así que tras
// cambiar la carpeta de datos las notificaciones saldrían sin ícono.
func forgetStaleIcon(iconPath string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appUserModelID,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if cur, _, err := k.GetStringValue("IconUri"); err == nil && cur != iconPath {
		k.DeleteValue("IconUri")
	}
}
