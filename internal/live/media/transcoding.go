package media

import (
	"github.com/go-playground/validator/v10"
	"github.com/muntader/zaynin-engine/internal/vod/types"
	"time"
)

type StreamStatus string

const (
	StreamStatusCreated StreamStatus = "created"
	StreamStatusActive  StreamStatus = "active"
	StreamStatusStopped StreamStatus = "stopped"
	StreamStatusError   StreamStatus = "error"
)

// StreamConfig is everything we need to spin up a live pipeline.
type StreamConfig struct {
	ID          string         `json:"id" validate:"required"`
	RTMPKey     string         `json:"rtmp_key" validate:"required"`
	Status      StreamStatus   `json:"status" validate:"required,oneof=created active stopped error"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	StoppedAt   *time.Time     `json:"stopped_at,omitempty"`
	WebhookURLs []string       `json:"webhook_urls,omitempty" validate:"dive,url"`
	Pipeline    PipelineConfig `json:"pipeline" validate:"required"`
}

type TranscodeConfig struct {
	Enabled bool `json:"enabled"`

	VideoRenditions []VideoRenditionConfig `json:"video_renditions,omitempty" validate:"required_if=Enabled true,dive"`

	AudioRenditions []AudioRenditionConfig `json:"audio_renditions,omitempty" validate:"dive"`

	Codec              string `json:"codec,omitempty" validate:"required_if=Enabled true,oneof=h264 hevc av1"`
	HardwarePreference string `json:"hardware_preference,omitempty" validate:"required_if=Enabled true,oneof=auto prefer_gpu force_cpu force_gpu"`
}
type VideoRenditionConfig struct {
	Width        int           `json:"width" validate:"required,gte=0"`
	Height       int           `json:"height" validate:"required,gte=0"`
	VideoBitrate int           `json:"video_bitrate" validate:"required,gt=0"`
	Framerate    int           `json:"framerate,omitempty"`
	Params       EncoderParams `json:"params,omitempty"`
}

type AudioRenditionConfig struct {
	InputTrackIndex int    `json:"input_track_index" validate:"gte=0"`
	Bitrate         int    `json:"bitrate" validate:"required,gt=0"`
	Codec           string `json:"codec" validate:"oneof=aac opus"`
	Language        string `json:"language" validate:"required"`
	Label           string `json:"label" validate:"required"`
}

type EncoderParams struct {
	Preset     *string `json:"preset,omitempty"`
	Tune       *string `json:"tune,omitempty"`
	Profile    *string `json:"profile,omitempty"`
	Level      *string `json:"level,omitempty"`
	Bframes    *int    `json:"bframes,omitempty"`
	Refs       *int    `json:"refs,omitempty"`
	Lookahead  *int    `json:"lookahead,omitempty"`
	TemporalAQ *bool   `json:"temporal_aq,omitempty"`
}

type PipelineConfig struct {
	Name         string          `json:"name" validate:"required"`
	RecordSource bool            `json:"record_source"`
	Transcode    TranscodeConfig `json:"transcode" validate:"required"`
	Package      PackageConfig   `json:"package" validate:"required"`
	Cleanup      CleanupConfig   `json:"cleanup"`
}

type PackageConfig struct {
	Enabled                            bool                  `json:"enabled"`
	EnableHls                          bool                  `json:"enable_hls"`
	EnableDash                         bool                  `json:"enable_dash"`
	LowLatencyEnabled                  bool                  `json:"low_latency_enabled"`
	SegmentDuration                    int                   `json:"segment_duration" validate:"required_if=Enabled true,gt=0"`
	FragmentDuration                   float64               `json:"fragment_duration" validate:"required_if=Enabled true,gt=0"`
	MinimumUpdatePeriod                int                   `json:"minimum_update_period"`
	SuggestedPresentationDelay         int                   `json:"suggested_presentation_delay"`
	TimeShiftBufferDepth               int                   `json:"time_shift_buffer_depth"`
	PreservedSegmentsOutsideLiveWindow int                   `json:"preserved_segments_outside_live_window"`
	DvrEnabled                         bool                  `json:"dvr_enabled"`
	Outputs                            []types.OutputStorage `json:"outputs,omitempty" validate:"omitempty,dive"`
}

type AESConfig struct {
	Enable       bool   `json:"enable"`
	KeyServerURL string `json:"key_server_url" validate:"required_if=Enable true"`
}

func PackageConfigValidation(sl validator.StructLevel) {
	pkgConfig := sl.Current().Interface().(PackageConfig)
	if pkgConfig.Enabled && !pkgConfig.EnableHls && !pkgConfig.EnableDash {
		sl.ReportError(pkgConfig.EnableHls, "enable_hls", "EnableHls", "hls_or_dash_required", "")
	}
}

type CleanupAction string

type PackagingCleanupConfig struct {
	Action CleanupAction `json:"action" validate:"required,oneof=delete keep"`
}

type CleanupConfig struct {
	Packaging PackagingCleanupConfig `json:"packaging"`
}
