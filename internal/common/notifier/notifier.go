package notifier

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/muntader/zaynin-engine/internal/common/types"
)

const (
	staticAuthWebhookURL = "http://localhost:3000/api/v1/live/auth"
)

// Notifiable is anything we can POST to the customer webhook.
type Notifiable interface {
	EventType() types.EventType
	JobID() string // stream id for live, job id for VOD
}

type Notifier struct {
	httpClient      *http.Client
	authWebhookURL  string
	notificationURL string
}

func New(notificationURL string) *Notifier {
	return &Notifier{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		authWebhookURL:  staticAuthWebhookURL,
		notificationURL: notificationURL,
	}
}

func (n *Notifier) SendNotification(payload Notifiable) {
	if n.notificationURL == "" {
		return // no URL configured   skip quietly
	}

	n.send(n.notificationURL, payload)
}

// SendAuthWebhook hits the internal auth service   separate from customer notifications.
func (n *Notifier) SendAuthWebhook(payload interface{}) {
	n.send(n.authWebhookURL, payload)
}

func (n *Notifier) send(url string, payload interface{}) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Notifier: failed to marshal payload", "error", err)
		return
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		slog.Error("Notifier: failed to create request", "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		slog.Error("Notifier: failed to send webhook", "url", url, "error", err)
		return
	}
	defer resp.Body.Close()

	logAttrs := []slog.Attr{
		slog.String("url", url),
		slog.String("status", resp.Status),
	}
	if p, ok := payload.(Notifiable); ok {
		logAttrs = append(logAttrs, slog.String("eventType", string(p.EventType())))
		logAttrs = append(logAttrs, slog.String("jobId", p.JobID()))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("Notifier: webhook received non-success status code", logAttrsToArgs(logAttrs)...)
	} else {
		slog.Debug("Notifier: webhook sent successfully", logAttrsToArgs(logAttrs)...)
	}
}

func logAttrsToArgs(attrs []slog.Attr) []any {
	args := make([]any, 0, len(attrs)*2)
	for _, attr := range attrs {
		args = append(args, attr.Key, attr.Value.Any())
	}
	return args
}
