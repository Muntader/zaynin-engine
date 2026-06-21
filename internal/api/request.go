package api

import (
	"github.com/muntader/zaynin-engine/internal/live/media"
)

// CreateStreamRequest is the POST /streams body   pipeline lives in the live package.
type CreateStreamRequest struct {
	Pipeline    media.PipelineConfig `json:"pipeline" validate:"required"`
	WebhookURLs []string             `json:"webhookUrls,omitempty" validate:"omitempty,dive,url"`
}
