package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/muntader/zaynin-engine/gen/centralpb"
	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
)

func (h *HandlerContext) reportStatus(ctx context.Context, jobID string, phase centralpb.VODJobPhase, message string) {
	if h.grpcCentral != nil {
		h.grpcCentral.ReportStatus(ctx, jobID, phase, message)
	}
}

func (h *HandlerContext) handleFailure(jobCtx *JobContext, failedAtPhase centralpb.VODJobPhase, err error) error {
	slog.Error("Job step failed", "job_id", jobCtx.Config.JobID, "phase", failedAtPhase.String(), "error", err)

	if h.grpcCentral != nil {
		h.grpcCentral.ReportFailure(jobCtx.Context, jobCtx.Config.JobID, failedAtPhase, err)
	}

	// failure webhooks not wired yet   central gRPC is the source of truth for now

	return err
}

func (h *HandlerContext) sendSuccessNotification(j *JobContext) {
	payload := configTypes.VODEventPayload{
		Timestamp: time.Now().UTC(),
		EvtType:   configTypes.VODJobCompleted,
		Type:      "vod",
		JobIDVal:  j.Config.JobID,
		JobLabel:  j.Config.JobLabel,
		Status:    configTypes.StatusCompleted,
	}
	j.Notifier.SendNotification(payload)
}

func (h *HandlerContext) sendFailureNotification(j *JobContext, jobErr error) {
	payload := configTypes.VODEventPayload{
		Timestamp: time.Now().UTC(),
		EvtType:   configTypes.VODJobFailed,
		Type:      "vod",
		JobIDVal:  j.Config.JobID,
		JobLabel:  j.Config.JobLabel,
		Status:    configTypes.StatusFailed,
		Error:     jobErr.Error(),
	}
	j.Notifier.SendNotification(payload)
}
