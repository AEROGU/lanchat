package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/AEROGU/lanchat/internal/app"
	"github.com/AEROGU/lanchat/internal/chat"
	"github.com/AEROGU/lanchat/internal/discovery"
	"github.com/AEROGU/lanchat/internal/protocol"
	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/version"
)

// Modo consola (-console): útil para pruebas y diagnóstico. Requiere el
// ejecutable de depuración (go tool mage debug), que sí tiene consola.

func runConsole(dir string, log *slog.Logger) error {
	a, err := app.New(app.Options{Dir: dir, Log: log})
	if err != nil {
		return err
	}
	self := a.Self()
	name := self.Name
	if name == "" {
		name = "(sin nombre)"
	}
	fmt.Printf("LanChat %s — %s · %s\nDatos en %s\nEscribe /ayuda para ver los comandos.\n\n",
		version.App, name, self.Hostname, self.Dir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := &console{app: a, quit: stop}
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for ev := range a.Events() {
			c.printEvent(ev)
		}
	}()
	go c.readLoop()

	err = a.Run(ctx)
	<-printed
	return err
}

type console struct {
	app  *app.App
	quit func()
	// list y rooms son las últimas listas mostradas; los comandos usan su numeración.
	list  []app.Contact
	rooms []app.Room
}

// historySize es cuántos mensajes muestra /hist.
const historySize = 20

const help = `Comandos:
  /lista              muestra los contactos numerados
  @N texto            envía "texto" al contacto N de la lista
  /hist N             últimos mensajes con el contacto N
  /nombre texto       cambia tu nombre (vacío = usar el hostname)
  /alias N texto      pone un alias local al contacto N (vacío = quitarlo)
  /grupo N nombre     pone al contacto N en un grupo (vacío = sin grupo)
  /confiar N          acepta la nueva identidad del contacto N (si cambió)
  /estado E [texto]   E = disponible, ausente u ocupado; texto opcional
  /todos texto        envía "texto" a todos los contactos en línea
  /salas              muestra las salas numeradas
  /sala nombre N,N    crea una sala con los contactos N de la lista
  #N texto            envía "texto" a la sala N
  /hsala N            últimos mensajes de la sala N
  /agregar N M,M      agrega los contactos M a la sala N
  /dejar N            sale de la sala N
  /salir              cierra LanChat`

func (c *console) readLoop() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := c.command(line); err != nil {
			fmt.Println("  error:", err)
		}
	}
}

func (c *console) command(line string) error {
	ctx := context.Background()
	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)

	switch {
	case cmd == "/ayuda" || cmd == "/?":
		fmt.Println(help)
	case cmd == "/salir":
		c.quit()
	case cmd == "/lista":
		return c.printList(ctx)
	case cmd == "/nombre":
		return c.app.SetName(rest)
	case cmd == "/estado":
		word, text, _ := strings.Cut(rest, " ")
		status, ok := consoleStatuses[strings.ToLower(word)]
		if !ok {
			return fmt.Errorf("usa: /estado disponible|ausente|ocupado [texto]")
		}
		return c.app.SetStatus(status, text)
	case cmd == "/hist":
		ct, err := c.pick(rest)
		if err != nil {
			return err
		}
		msgs, err := c.app.History(ctx, ct.ID, "", historySize)
		if err != nil {
			return err
		}
		fmt.Printf("── %s (%s) ──\n", ct.DisplayName(), ct.Detail())
		for _, m := range msgs {
			who := ct.DisplayName()
			if m.Outgoing {
				who = "Yo"
			}
			fmt.Printf("  [%s] %s: %s%s\n", m.At.Format("02/01 15:04"), who, m.Body, statusMark(m))
		}
	case cmd == "/confiar":
		ct, err := c.pick(rest)
		if err != nil {
			return err
		}
		return c.app.TrustIdentity(ctx, ct.ID)
	case cmd == "/grupo":
		n, group, _ := strings.Cut(rest, " ")
		ct, err := c.pick(n)
		if err != nil {
			return err
		}
		return c.app.SetGroup(ctx, ct.ID, group)
	case cmd == "/alias":
		n, alias, _ := strings.Cut(rest, " ")
		ct, err := c.pick(n)
		if err != nil {
			return err
		}
		return c.app.SetAlias(ctx, ct.ID, alias)
	case cmd == "/todos":
		list, err := c.app.Contacts(ctx)
		if err != nil {
			return err
		}
		var ids []string
		for _, ct := range list {
			if ct.Online {
				ids = append(ids, ct.ID)
			}
		}
		msgs, err := c.app.SendMany(ctx, ids, rest)
		fmt.Printf("  enviado a %d\n", len(msgs))
		return err
	case cmd == "/salas":
		return c.printRooms(ctx)
	case cmd == "/sala":
		i := strings.LastIndex(rest, " ")
		if i < 0 {
			return fmt.Errorf("usa: /sala nombre N,N")
		}
		ids, err := c.pickMany(rest[i+1:])
		if err != nil {
			return err
		}
		_, err = c.app.CreateRoom(ctx, rest[:i], ids)
		return err
	case cmd == "/agregar":
		n, list, _ := strings.Cut(rest, " ")
		r, err := c.pickRoom(n)
		if err != nil {
			return err
		}
		ids, err := c.pickMany(list)
		if err != nil {
			return err
		}
		return c.app.AddRoomMembers(ctx, r.ID, ids)
	case cmd == "/dejar":
		r, err := c.pickRoom(rest)
		if err != nil {
			return err
		}
		return c.app.LeaveRoom(ctx, r.ID)
	case cmd == "/hsala":
		r, err := c.pickRoom(rest)
		if err != nil {
			return err
		}
		msgs, err := c.app.RoomHistory(ctx, r.ID, "", historySize)
		if err != nil {
			return err
		}
		fmt.Printf("── %s ──\n", r.Name)
		for _, m := range msgs {
			fmt.Printf("  [%s] %s%s\n", m.At.Format("02/01 15:04"), c.roomLine(ctx, m), statusMark(m))
		}
	case strings.HasPrefix(cmd, "#"):
		r, err := c.pickRoom(cmd[1:])
		if err != nil {
			return err
		}
		_, err = c.app.SendRoom(ctx, r.ID, rest)
		return err
	case strings.HasPrefix(cmd, "@"):
		ct, err := c.pick(cmd[1:])
		if err != nil {
			return err
		}
		_, err = c.app.Send(ctx, ct.ID, rest)
		return err
	default:
		return fmt.Errorf("comando desconocido; escribe /ayuda")
	}
	return nil
}

func (c *console) printList(ctx context.Context) error {
	list, err := c.app.Contacts(ctx)
	if err != nil {
		return err
	}
	c.list = list
	if len(list) == 0 {
		fmt.Println("  (aún no se ha visto ningún equipo)")
	}
	for i, ct := range list {
		state := "○"
		if ct.Online {
			state = "●"
		}
		status := ""
		if ct.Online {
			status = statusNames[ct.Status]
			if ct.StatusText != "" {
				status += ": " + ct.StatusText
			}
			status = "  [" + status + "]"
		}
		if ct.IdentityChanged() {
			status += "  ⚠ su identidad cambió (/confiar " + strconv.Itoa(i+1) + ")"
		}
		fmt.Printf("  %2d %s %s  (%s)%s\n", i+1, state, ct.DisplayName(), ct.Detail(), status)
	}
	return nil
}

func (c *console) pick(s string) (app.Contact, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(c.list) {
		return app.Contact{}, fmt.Errorf("número de contacto inválido; usa /lista")
	}
	return c.list[n-1], nil
}

// pickMany interpreta "1,3,4" como contactos de la última /lista.
func (c *console) pickMany(s string) ([]string, error) {
	var ids []string
	for n := range strings.SplitSeq(s, ",") {
		ct, err := c.pick(strings.TrimSpace(n))
		if err != nil {
			return nil, err
		}
		ids = append(ids, ct.ID)
	}
	return ids, nil
}

func (c *console) printRooms(ctx context.Context) error {
	rooms, err := c.app.Rooms(ctx)
	if err != nil {
		return err
	}
	c.rooms = rooms
	if len(rooms) == 0 {
		fmt.Println("  (no estás en ninguna sala; créala con /sala)")
	}
	for i, r := range rooms {
		names := make([]string, len(r.Members))
		for j, id := range r.Members {
			names[j] = c.name(ctx, id)
		}
		note := ""
		if r.Left {
			note = "  (saliste)"
		}
		fmt.Printf("  %2d %s: %s%s\n", i+1, r.Name, strings.Join(names, ", "), note)
	}
	return nil
}

func (c *console) pickRoom(s string) (app.Room, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > len(c.rooms) {
		return app.Room{}, fmt.Errorf("número de sala inválido; usa /salas")
	}
	return c.rooms[n-1], nil
}

// name es el nombre de un equipo tal como se muestra aquí.
func (c *console) name(ctx context.Context, id string) string {
	if id == c.app.Self().ID {
		return "Yo"
	}
	if ct, ok, _ := c.app.Contact(ctx, id); ok {
		return ct.DisplayName()
	}
	return id
}

// roomLine es "Autor: texto", o "· aviso" para los avisos de la sala.
func (c *console) roomLine(ctx context.Context, m store.Message) string {
	if m.Kind == store.KindRoomEvent {
		return "· " + m.Body
	}
	who := "Yo"
	if !m.Outgoing {
		who = c.name(ctx, m.PeerID)
	}
	return who + ": " + m.Body
}

func (c *console) printEvent(ev any) {
	ctx := context.Background()
	switch e := ev.(type) {
	case discovery.Event:
		ct, _, _ := c.app.Contact(ctx, e.Peer.ID)
		fmt.Printf("[%s] %s (%s)\n", e.Type, ct.DisplayName(), ct.Detail())
	case chat.Event:
		if e.Type == chat.RoomChanged || e.Message.RoomID != "" {
			c.printRoomEvent(ctx, e)
			return
		}
		ct, _, _ := c.app.Contact(ctx, e.Message.PeerID)
		switch e.Type {
		case chat.MessageReceived:
			fmt.Printf("\a[%s] %s: %s\n", e.Message.At.Format("15:04"), ct.DisplayName(), e.Message.Body)
		case chat.MessageQueued:
			if !ct.Online {
				fmt.Printf("  … %s está desconectado; se entregará cuando se conecte\n", ct.DisplayName())
			}
		case chat.MessageDelivered:
			fmt.Printf("  ✓ entregado a %s\n", ct.DisplayName())
		}
	}
}

func (c *console) printRoomEvent(ctx context.Context, e chat.Event) {
	m := e.Message
	r, _, _ := c.app.Room(ctx, m.RoomID)
	switch e.Type {
	case chat.MessageReceived:
		fmt.Printf("\a[%s] %s · %s\n", m.At.Format("15:04"), r.Name, c.roomLine(ctx, m))
	case chat.MessageDelivered:
		fmt.Printf("  ✓ entregado a todos en %s\n", r.Name)
	}
}

func statusMark(m store.Message) string {
	if m.Outgoing && m.Status == store.StatusPending {
		return "  (pendiente)"
	}
	return ""
}

var consoleStatuses = map[string]string{
	"disponible": protocol.StatusAvailable,
	"ausente":    protocol.StatusAway,
	"ocupado":    protocol.StatusBusy,
}

var statusNames = map[string]string{
	protocol.StatusAvailable: "disponible",
	protocol.StatusAway:      "ausente",
	protocol.StatusBusy:      "ocupado",
}
