package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muntader/zaynin-engine/gen/centralpb"
	"github.com/muntader/zaynin-engine/internal/common/central_api_client"
	"github.com/muntader/zaynin-engine/internal/common/notifier"
	zayninengineTypes "github.com/muntader/zaynin-engine/pkg/encoder/types"

	"github.com/hibiken/asynq"

	"github.com/muntader/zaynin-engine/internal/vod/queue"
	"github.com/muntader/zaynin-engine/internal/vod/service"
	"github.com/redis/go-redis/v9"
)

// HandlerContext holds long-lived deps shared by every asynq task handler.
type HandlerContext struct {
	client            *asynq.Client
	redisClient       *redis.Client
	grpcCentral       *central_api_client.Client
	workspaceBasePath string
	storageSvc        *service.StorageService
	notifier          *notifier.Notifier
}

// RegisterVODHandlers wires the VOD workflow onto the CPU and I/O asynq muxes.
func RegisterVODHandlers(
	cpuMux, ioMux *asynq.ServeMux,
	client *asynq.Client,
	redisClient *redis.Client,
	grpcCentral *central_api_client.Client,
	workspaceBasePath string,
	storageSvc *service.StorageService,
	notifier *notifier.Notifier,
) {
	slog.Info("Registering VOD handlers for a sequential workflow")

	ctx := &HandlerContext{
		client:            client,
		redisClient:       redisClient,
		grpcCentral:       grpcCentral,
		workspaceBasePath: workspaceBasePath,
		storageSvc:        storageSvc,
		notifier:          notifier,
	}

	// I/O-bound steps
	ioMux.HandleFunc(queue.TypeDownloadSource, ctx.HandleDownloadSource)
	ioMux.HandleFunc(queue.TypeUploadOutput, ctx.HandleUploadOutput)
	ioMux.HandleFunc(queue.TypeCleanupWorkspace, ctx.HandleCleanupWorkspace)
	ioMux.HandleFunc(queue.TypeVodRecording, ctx.HandleVODFinalize)
	// CPU-bound steps
	cpuMux.HandleFunc(queue.TypeAnalyzeMedia, ctx.HandleAnalyzeMedia)
	cpuMux.HandleFunc(queue.TypeRunEncoding, ctx.HandleRunEncoding)
	cpuMux.HandleFunc(queue.TypeGenerateThumbnail, ctx.HandleProcessThumbnails)
	cpuMux.HandleFunc(queue.TypeGenerateGIF, ctx.HandleProcessGIFs)
	cpuMux.HandleFunc(queue.TypeFragmentAndPackage, ctx.HandleFragmentAndPackage)
}

// HandleDownloadSource sets up the workspace, fetches input, then enqueues analysis.
func (h *HandlerContext) HandleDownloadSource(ctx context.Context, t *asynq.Task) error {
	var p queue.StartWorkflowPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeDownloadSource, err)
	}

	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_DOWNLOADING, "Starting file download.")

	workspacePath, err := prepareWorkspace(p.Config, h.workspaceBasePath)
	if err != nil {
		slog.Error("Failed to prepare workspace", "job_id", p.Config.JobID, "error", err)
		return err
	}

	jobCtx := &JobContext{
		Context:        ctx,
		Config:         p.Config,
		WorkspacePath:  workspacePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
	}

	var sourcePath string

	// local input: skip copy   just point at the file on disk
	if p.Config.InputStorage.Provider == "local" && p.Config.InputStorage.Local != nil {
		sourcePath = p.Config.InputStorage.Local.Path

		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_DOWNLOADING, err)
		}

	} else {
		downloadedPath, err := downloadSource(jobCtx)
		if err != nil {
			return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_DOWNLOADING, err)
		}
		sourcePath = downloadedPath
	}

	// CHAIN to analyze
	nextPayload := queue.WorkflowStatePayload{
		Config:        p.Config,
		WorkspacePath: workspacePath,
		SourcePath:    sourcePath,
	}
	nextTask, err := queue.NewTask(queue.TypeAnalyzeMedia, nextPayload, queue.QueueNameEncoder)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}

	_, err = h.client.EnqueueContext(ctx, nextTask)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}

	return nil
}

// HandleAnalyzeMedia runs ffprobe/planning and hands the report to encoding.
func (h *HandlerContext) HandleAnalyzeMedia(ctx context.Context, t *asynq.Task) error {
	var p queue.WorkflowStatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeAnalyzeMedia, err)
	}

	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_TRANSCODING, "Starting media analysis.")

	jobCtx := &JobContext{
		Context: ctx,
		Config:  p.Config,
		AnalysisConfig: zayninengineTypes.AnalysisConfig{
			Outputs:     &p.Config.Outputs,
			JobSettings: p.Config.JobSettings,
		},
		WorkspacePath:  p.WorkspacePath,
		AnalysisReport: p.AnalysisReport,
		SourcePath:     p.SourcePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
	}

	if err := analyzeMedia(jobCtx); err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}

	p.AnalysisReport = jobCtx.AnalysisReport

	nextTask, err := queue.NewTask(queue.TypeRunEncoding, p, queue.QueueNameEncoder)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}
	_, err = h.client.EnqueueContext(ctx, nextTask)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}
	return nil
}

// HandleRunEncoding transcodes renditions/tracks, then queues thumbnail work.
func (h *HandlerContext) HandleRunEncoding(ctx context.Context, t *asynq.Task) error {
	var p queue.WorkflowStatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeRunEncoding, err)
	}

	details := fmt.Sprintf("Starting encoding of %d video rendition(s) and %d audio track(s).",
		len(p.AnalysisReport.Video.Renditions),
		len(p.AnalysisReport.Audio),
	)
	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_TRANSCODING, details)

	jobCtx := &JobContext{
		Context:        ctx,
		Config:         p.Config,
		WorkspacePath:  p.WorkspacePath,
		SourcePath:     p.SourcePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
		AnalysisReport: p.AnalysisReport,
	}

	if err := runEncoding(jobCtx); err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}

	nextTask, err := queue.NewTask(queue.TypeGenerateThumbnail, p, queue.QueueNameEncoder)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}
	_, err = h.client.EnqueueContext(ctx, nextTask)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}
	return nil
}

// HandleProcessThumbnails runs each enabled thumb config, then chains to GIFs.
func (h *HandlerContext) HandleProcessThumbnails(ctx context.Context, t *asynq.Task) error {
	var p queue.WorkflowStatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeGenerateThumbnail, err)
	}
	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_TRANSCODING, "Generating thumbnails.")

	jobCtx := &JobContext{
		Context:        ctx,
		Config:         p.Config,
		WorkspacePath:  p.WorkspacePath,
		SourcePath:     p.SourcePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
		AnalysisReport: p.AnalysisReport,
	}

	// thumbnails are quick   serial is fine and keeps disk IO predictable
	for _, thumbCfg := range p.Config.Outputs.Thumbnails {
		if thumbCfg.Enable {
			if err := generateThumbnails(jobCtx, thumbCfg); err != nil {
				if thumbCfg.AllowSoftFail {
					slog.Warn("Thumbnail generation failed, but soft-fail is enabled. Continuing.", "job_id", p.Config.JobID, "thumbnail_id", thumbCfg.ID, "error", err)
				} else {
					return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
				}
			}
		}
	}

	nextTask, err := queue.NewTask(queue.TypeGenerateGIF, p, queue.QueueNameEncoder)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}
	_, err = h.client.EnqueueContext(ctx, nextTask)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
	}
	return nil
}

func (h *HandlerContext) HandleProcessGIFs(ctx context.Context, t *asynq.Task) error {
	var p queue.WorkflowStatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeGenerateGIF, err)
	}

	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_TRANSCODING, "Generating GIF.")

	jobCtx := &JobContext{
		Context:        ctx,
		Config:         p.Config,
		WorkspacePath:  p.WorkspacePath,
		SourcePath:     p.SourcePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
		AnalysisReport: p.AnalysisReport,
	}

	for _, gifCfg := range p.Config.Outputs.AnimatedGIFs {
		if gifCfg.Enable {
			if err := generateGIF(jobCtx, gifCfg); err != nil {
				if gifCfg.AllowSoftFail {
					slog.Warn("GIF generation failed, but soft-fail is enabled. Continuing.", "job_id", p.Config.JobID, "gif_id", gifCfg.ID, "error", err)
				} else {
					return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_TRANSCODING, err)
				}
			}
		}
	}

	// skip packaging when the job only wanted sidecar assets
	if p.Config.Outputs.StreamingPackage != nil && p.Config.Outputs.StreamingPackage.Enable {
		nextTask, err := queue.NewTask(queue.TypeFragmentAndPackage, p, queue.QueueNameEncoder)
		if err != nil {
			return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_PACKAGING, err)
		}
		_, err = h.client.EnqueueContext(ctx, nextTask)
		if err != nil {
			return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_PACKAGING, err)
		}
	} else {
		nextTask, err := queue.NewTask(queue.TypeUploadOutput, p, queue.QueueNameGeneral)
		if err != nil {
			return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_UPLOADING, err)
		}
		_, err = h.client.EnqueueContext(ctx, nextTask)
		if err != nil {
			return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_UPLOADING, err)
		}
	}
	return nil
}

// HandleFragmentAndPackage segments + packages, then enqueues upload.
func (h *HandlerContext) HandleFragmentAndPackage(ctx context.Context, t *asynq.Task) error {
	var p queue.WorkflowStatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeFragmentAndPackage, err)
	}

	formats := strings.Join(p.Config.Outputs.StreamingPackage.Packaging.Formats, ", ")
	details := fmt.Sprintf("Starting fragmentation and packaging for formats: [%s].", formats)
	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_PACKAGING, details)

	jobCtx := &JobContext{
		Context: ctx,
		Config:  p.Config,
		AnalysisConfig: zayninengineTypes.AnalysisConfig{
			Outputs:     &p.Config.Outputs,
			JobSettings: p.Config.JobSettings,
		},
		WorkspacePath:  p.WorkspacePath,
		SourcePath:     p.SourcePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
		AnalysisReport: p.AnalysisReport,
	}

	if err := fragmentAndPackage(jobCtx); err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_PACKAGING, err)
	}

	nextTask, err := queue.NewTask(queue.TypeUploadOutput, p, queue.QueueNameGeneral)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_UPLOADING, err)
	}
	_, err = h.client.EnqueueContext(ctx, nextTask)
	if err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_UPLOADING, err)
	}
	return nil
}

// HandleUploadOutput pushes outputs, notifies success, schedules delayed cleanup.
func (h *HandlerContext) HandleUploadOutput(ctx context.Context, t *asynq.Task) error {
	var p queue.WorkflowStatePayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeUploadOutput, err)
	}

	details := fmt.Sprintf("Starting upload to provider: %s.", p.Config.OutputStorage.Provider)
	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_UPLOADING, details)

	jobCtx := &JobContext{
		Context:        ctx,
		Config:         p.Config,
		WorkspacePath:  p.WorkspacePath,
		StorageService: h.storageSvc,
		Notifier:       h.notifier,
	}

	if err := uploadOutput(jobCtx); err != nil {
		return h.handleFailure(jobCtx, centralpb.VODJobPhase_PHASE_UPLOADING, err)
	}

	slog.Info("VOD job workflow succeeded. Sending notification.", "job_id", p.Config.JobID)
	h.sendSuccessNotification(jobCtx)

	// delay cleanup so support can grab logs from the workspace if something looks off
	cleanupPayload := queue.CleanupPayload{WorkspacePath: p.WorkspacePath}
	cleanupTask, err := queue.NewTask(queue.TypeCleanupWorkspace, cleanupPayload, queue.QueueNameGeneral)
	if err != nil {
		slog.Error("Failed to create final cleanup task", "job_id", p.Config.JobID, "error", err)
		return nil
	}

	_, err = h.client.EnqueueContext(ctx, cleanupTask, asynq.ProcessIn(1*time.Minute))
	if err != nil {
		slog.Error("Failed to enqueue final cleanup task", "job_id", p.Config.JobID, "error", err)
	}

	h.reportStatus(ctx, p.Config.JobID, centralpb.VODJobPhase_PHASE_COMPLETED, "Job completed successfully.")

	return nil
}

// HandleCleanupWorkspace removes the per-job workspace directory after a successful upload.
func (h *HandlerContext) HandleCleanupWorkspace(ctx context.Context, t *asynq.Task) error {
	var p queue.CleanupPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("invalid payload for %s: %w", queue.TypeCleanupWorkspace, err)
	}
	if p.WorkspacePath == "" {
		return nil
	}
	if err := os.RemoveAll(p.WorkspacePath); err != nil {
		return fmt.Errorf("cleanup workspace %s: %w", p.WorkspacePath, err)
	}
	slog.Info("VOD workspace cleaned up", "path", p.WorkspacePath)
	return nil
}

// HandleVODFinalize turns a live recording tree into VOD playlists and uploads it.
func (h *HandlerContext) HandleVODFinalize(ctx context.Context, t *asynq.Task) error {
	var p queue.VODRecordingPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("FATAL: failed to unmarshal VOD finalize payload: %w", err)
	}

	slog.Info("Starting VOD finalization", "streamID", p.StreamID, "source", p.SourcePath)

	err := createVODManifests(p.SourcePath)
	if err != nil {
		slog.Error("Failed to create VOD manifests", "streamID", p.StreamID, "error", err)
		return err
	}

	var firstError error
	for _, output := range p.Outputs {
		err := h.storageSvc.UploadDirectory(ctx, output, p.SourcePath)
		if err != nil {
			slog.Error("Failed to upload VOD to output", "streamID", p.StreamID, "outputID", output.OutputID, "error", err)
			if firstError == nil {
				firstError = err
			}
			// keep going   other destinations might still work
		}
	}

	if firstError != nil {
		return firstError
	}

	if err := os.RemoveAll(p.SourcePath); err != nil {
		slog.Error("Failed to clean up local VOD source directory", "path", p.SourcePath, "error", err)
		// upload already succeeded; dont retry the whole job becasue cleanup failed
	}

	return nil
}

// createVODManifests rewrites live HLS playlists so players know the event ended.
func createVODManifests(dir string) error {
	masterPlaylistPath := filepath.Join(dir, "master.m3u8")
	if _, err := os.Stat(masterPlaylistPath); os.IsNotExist(err) {
		slog.Warn("master.m3u8 not found, cannot create VOD manifest.", "path", masterPlaylistPath)
		return nil
	}

	masterContent, err := os.ReadFile(masterPlaylistPath)
	if err != nil {
		return fmt.Errorf("could not read master playlist: %w", err)
	}

	lines := strings.Split(string(masterContent), "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasSuffix(trimmedLine, ".m3u8") {
			renditionPath := filepath.Join(dir, trimmedLine)
			err := convertLivePlaylistToVOD(renditionPath)
			if err != nil {
				slog.Error("Failed to convert rendition playlist to VOD", "path", renditionPath, "error", err)
			}
		}
	}
	return nil
}

// convertLivePlaylistToVOD strips live-only tags and ensures #EXT-X-ENDLIST is present.
func convertLivePlaylistToVOD(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read playlist file %s: %w", path, err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	hasEndlist := false

	if len(lines) > 0 {
		newLines = append(newLines, lines[0])
	}
	newLines = append(newLines, "#EXT-X-PLAYLIST-TYPE:VOD")

	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE") || strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE") {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-ENDLIST") {
			hasEndlist = true
		}
		newLines = append(newLines, line)
	}

	if !hasEndlist {
		newLines = append(newLines, "#EXT-X-ENDLIST")
	}

	return os.WriteFile(path, []byte(strings.Join(newLines, "\n")), 0644)
}
