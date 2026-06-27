// Package notify sends desktop and optional Telegram notifications for key
// events (spec §8.3/OB-7): a command needs approval, a job failed, an agent
// connected. External channels are the one explicit exception to local-first and
// are opt-in. Notification content is best-effort minimal and, because the bus is
// not redacted at the source, every message is run through the redactor before it
// leaves the box (Telegram especially) so a secret in a command line / prompt is
// masked rather than shipped to a third party.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/termada/termada/internal/bus"
)

// Redactor masks secrets in notification text before it leaves the box.
type Redactor interface{ Redact(string) string }

// Notifier delivers notifications for selected event types.
type Notifier struct {
	desktop  bool
	telegram TelegramConfig
	redactor Redactor
	http     *http.Client
}

// TelegramConfig configures the optional Telegram channel.
type TelegramConfig struct {
	Enabled  bool
	BotToken string
	ChatID   string
}

// New builds a notifier. redactor may be nil (no masking).
func New(desktop bool, tg TelegramConfig, redactor Redactor) *Notifier {
	return &Notifier{desktop: desktop, telegram: tg, redactor: redactor, http: &http.Client{Timeout: 8 * time.Second}}
}

// Subscribe consumes bus events and notifies on the interesting ones. It runs
// until the channel closes.
func (n *Notifier) Subscribe(ch <-chan bus.Event) {
	for e := range ch {
		title, body, ok := interesting(e)
		if !ok {
			continue
		}
		n.send(title, body)
	}
}

func interesting(e bus.Event) (title, body string, ok bool) {
	switch e.Type {
	case bus.EvConfirmRequested:
		return "Termada: approval needed", e.Message, true
	case bus.EvPolicyDenied:
		return "Termada: command denied", e.Message, true
	case bus.EvJobFinished:
		if s, _ := e.Data["status"].(string); s == "failed" || s == "killed" || s == "timed_out" {
			return "Termada: job " + s, e.Message, true
		}
	case bus.EvAgentConnected:
		return "Termada: agent connected", e.AgentID, true
	}
	return "", "", false
}

func (n *Notifier) send(title, body string) {
	if n.redactor != nil {
		title = n.redactor.Redact(title)
		body = n.redactor.Redact(body)
	}
	if n.desktop {
		n.desktopNotify(title, body)
	}
	if n.telegram.Enabled && n.telegram.BotToken != "" && n.telegram.ChatID != "" {
		n.telegramNotify(title + "\n" + body)
	}
}

// desktopNotify uses the platform's native mechanism, shelling out (no CGO).
func (n *Notifier) desktopNotify(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		_ = exec.Command("osascript", "-e", script).Run()
	case "linux":
		_ = exec.Command("notify-send", title, body).Run()
	}
}

func (n *Notifier) telegramNotify(text string) {
	api := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.telegram.BotToken)
	payload, _ := json.Marshal(map[string]string{"chat_id": n.telegram.ChatID, "text": text})
	req, err := http.NewRequest(http.MethodPost, api, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}
