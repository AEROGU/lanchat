package app

import (
	"context"
	"errors"
	"fmt"
)

// Privacidad: descargar una copia de los datos y borrarlos (p. ej. cuando el
// usuario deja la empresa). Borrar es solo en este equipo: los demás
// conservan su copia de las conversaciones.

// ExportData escribe en path (que no debe existir) una copia de la base de
// datos con el historial, contactos, salas y ofertas de archivos.
func (a *App) ExportData(ctx context.Context, path string) error {
	return a.store.Backup(ctx, path)
}

// DeleteConversation borra la conversación con peerID y cancela los envíos
// de archivos sin terminar con ese contacto. El contacto se conserva.
func (a *App) DeleteConversation(ctx context.Context, peerID string) error {
	if err := a.transfer.CancelAll(ctx, peerID); err != nil {
		return fmt.Errorf("cancelando los archivos pendientes: %w", err)
	}
	return a.store.DeleteConversation(ctx, peerID)
}

// DeleteRoomConversation borra los mensajes de la sala. Si este equipo ya
// salió de ella, la sala desaparece de la lista; si no, sigue (vacía). El
// aviso de salida, si aún no llegó a todos, se sigue entregando.
func (a *App) DeleteRoomConversation(ctx context.Context, roomID string) error {
	r, ok, err := a.store.Room(ctx, roomID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("sala inexistente")
	}
	return a.store.DeleteRoomConversation(ctx, roomID, r.Left)
}

// WipeData borra todos los datos del usuario: conversaciones, salas, archivos
// pendientes, alias y grupos de contactos, su nombre y su mensaje de estado.
// Conserva la configuración de red y la identidad de este equipo. Los
// archivos ya recibidos quedan en su carpeta.
func (a *App) WipeData(ctx context.Context) error {
	if err := a.transfer.CancelAll(ctx, ""); err != nil {
		return fmt.Errorf("cancelando los archivos pendientes: %w", err)
	}
	// Salir de las salas avisa a los demás, que dejan de enviar mensajes aquí.
	rooms, err := a.store.Rooms(ctx)
	if err != nil {
		return err
	}
	for _, r := range rooms {
		if !r.Left {
			if err := a.LeaveRoom(ctx, r.ID); err != nil {
				return fmt.Errorf("saliendo de la sala «%s»: %w", r.Name, err)
			}
		}
	}
	if err := a.store.Wipe(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Name, a.cfg.StatusText = "", ""
	err = a.cfg.Save(a.dir)
	a.mu.Unlock()
	a.disc.SetName("")
	a.applyStatus()
	return err
}
