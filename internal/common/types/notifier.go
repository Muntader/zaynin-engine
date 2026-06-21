package types

import (
	"time"

	"github.com/muntader/zaynin-engine/internal/live/media"
)

type EventType string

const (
	StreamStarted EventType = "stream.started"
	StreamStopped EventType = "stream.stopped"
	WebhookFailed EventType = "webhook.failed"
)

const (
	VODJobCreated    EventType = "vod.job.created"
	VODJobProcessing EventType = "vod.job.processing"
	VODJobCompleted  EventType = "vod.job.completed"
	VODJobFailed     EventType = "vod.job.failed"
)

type JobStatus string

const (
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
)

type LiveEventPayload struct {
	Timestamp    time.Time           `json:"timestamp"`
	EvtType      EventType           `json:"eventType"`
	StreamID     string              `json:"streamId"`
	NodeID       string              `json:"nodeId"`
	StreamConfig *media.StreamConfig `json:"streamConfig,omitempty"`
	Metadata     map[string]string   `json:"metadata,omitempty"`
}

func (p LiveEventPayload) EventType() EventType { return p.EvtType }

func (p LiveEventPayload) JobID() string { return p.StreamID }

type VODEventPayload struct {
	Timestamp time.Time `json:"timestamp"`
	EvtType   EventType `json:"eventType"`
	Type      string    `json:"type"`
	JobIDVal  string    `json:"jobId"`
	JobLabel  string    `json:"jobLabel,omitempty"`
	Status    JobStatus `json:"status"`
	Error     string    `json:"error,omitempty"`
}

type NotificationTarget struct {
	WebhookURL string
	AuthToken  string
}

func (p VODEventPayload) EventType() EventType { return p.EvtType }

func (p VODEventPayload) JobID() string { return p.JobIDVal }
