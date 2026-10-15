package app

import (
	"cmp"
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

// RemoveContact quita de la lista a un contacto desconectado: típicamente una
// identidad vieja de alguien que reinstaló LanChat y ya aparece con otra. Su
// conversación se combina con la de into o, con into vacío, se borra. Si ese
// equipo vuelve a conectarse, aparece de nuevo.
func (a *App) RemoveContact(ctx context.Context, peerID, into string) error {
	if p, ok := a.disc.Peer(peerID); ok && p.Online {
		return errors.New("el contacto está conectado: solo se pueden quitar los desconectados")
	}
	if _, ok, err := a.store.Peer(ctx, peerID); err != nil || !ok {
		return cmp.Or(err, fmt.Errorf("contacto %s desconocido", peerID))
	}
	if into != "" {
		target, ok, err := a.store.Peer(ctx, into)
		switch {
		case err != nil:
			return err
		case into == peerID:
			return errors.New("no se puede combinar la conversación consigo misma")
		case !ok || target.Removed:
			return fmt.Errorf("contacto %s desconocido", into)
		}
	}
	if err := a.transfer.CancelAll(ctx, peerID); err != nil {
		return fmt.Errorf("cancelando los archivos pendientes: %w", err)
	}
	if err := a.store.RemovePeer(ctx, peerID, into); err != nil {
		return err
	}
	a.disc.Forget(peerID)
	if into != "" {
		a.chat.Flush(into) // los mensajes pendientes ahora son para into
	}
	return nil
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
