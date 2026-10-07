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
	// list es la última lista mostrada; los comandos usan su numeración.
	list []app.Contact
}

// historySize es cuántos mensajes muestra /hist.
const historySize = 20

const help = `Comandos:
  /lista              muestra los contactos numerados
  @N texto            envía "texto" al contacto N de la lista
  /hist N             últimos mensajes con el contacto N
  /nombre texto       cambia tu nombre (vacío = usar el hostname)
  /alias N texto      pone un alias local al contacto N (vacío = quitarlo)
  /estado E [texto]   E = disponible, ausente u ocupado; texto opcional
  /todos texto         envía "texto" a todos los contactos en línea
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

func (c *console) printEvent(ev any) {
	ctx := context.Background()
	switch e := ev.(type) {
	case discovery.Event:
		ct, _, _ := c.app.Contact(ctx, e.Peer.ID)
		fmt.Printf("[%s] %s (%s)\n", e.Type, ct.DisplayName(), ct.Detail())
	case chat.Event:
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
