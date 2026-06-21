package forward

import (
	"time"
)

type AddPusherRequest struct {
	Platform  string `json:"platform" validate:"required,alphanum"`
	RemoteURL string `json:"remote_url" validate:"required,url"`
	APIKey    string `json:"api_key" validate:"required"`
}

type PusherConfig struct {
	ID        string `json:"id"`
	StreamKey string `json:"stream_key"`
	Platform  string `json:"platform"`
	RemoteURL string `json:"remote_url"`
	APIKey    string `json:"api_key"`
	Enabled   bool   `json:"enabled"`
}

type PusherStats struct {
	PacketsReceived uint64    `json:"packets_received"`
	PacketsSent     uint64    `json:"packets_sent"`
	PacketsDropped  uint64    `json:"packets_dropped"`
	ReconnectCount  uint64    `json:"reconnect_count"`
	LastError       string    `json:"last_error,omitempty"` // string so json doesnt choke on error types
	LastErrorTime   time.Time `json:"last_error_time,omitempty"`
}

type PusherStatus struct {
	Config PusherConfig `json:"config"`
	State  string       `json:"state"` // plain string   api doesnt need the internal enum
	Stats  PusherStats  `json:"stats"`
}
