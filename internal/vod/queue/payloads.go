package queue

import (
	"github.com/muntader/zaynin-engine/internal/vod/types"
	zayninengineTypes "github.com/muntader/zaynin-engine/pkg/encoder/types"
)

// StartWorkflowPayload is what the API enqueues to kick off a VOD job.
type StartWorkflowPayload struct {
	Config types.Config `json:"config"`
}

// WorkflowStatePayload carries job state between asynq steps in the chain.
type WorkflowStatePayload struct {
	Config         types.Config                    `json:"config"`
	AnalysisReport *zayninengineTypes.AnalysisReport `json:"analysis_report,omitempty"`
	WorkspacePath  string                          `json:"workspacePath"`
	SourcePath     string                          `json:"sourcePath"`
}

type GenerateThumbnailPayload struct {
	WorkflowStatePayload
	ThumbnailConfig types.Thumbnail
}

type GenerateGIFPayload struct {
	WorkflowStatePayload
	GIFConfig types.AnimatedGIF
}

type GenerateClipPayload struct {
	WorkflowStatePayload
	ClipConfig types.Clip
}

type CleanupPayload struct {
	WorkspacePath string `json:"workspacePath"`
}

type VODRecordingPayload struct {
	StreamID   string                `json:"stream_id"`
	SessionID  string                `json:"session_id"`
	SourcePath string                `json:"source_path"` // local HLS tree from live recording
	Outputs    []types.OutputStorage `json:"outputs"`
}
