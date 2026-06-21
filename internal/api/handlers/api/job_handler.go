package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/muntader/zaynin-engine/internal/vod/queue"
	"github.com/muntader/zaynin-engine/internal/vod/types"

	"github.com/go-playground/validator/v10"
	"github.com/gorilla/mux"
	"github.com/hibiken/asynq"
)

type JobHandler struct {
	queueClient *asynq.Client
	validate    *validator.Validate
}

func NewJobHandler(client *asynq.Client) *JobHandler {
	return &JobHandler{
		queueClient: client,
		validate:    validator.New(),
	}
}

// CreateJob validates the job JSON and enqueues the first workflow step (download).
func (h *JobHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var jobConfig types.Config
	if err := json.NewDecoder(r.Body).Decode(&jobConfig); err != nil {
		slog.Error("Failed to decode job config from request", "error", err)
		http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.validate.Struct(jobConfig); err != nil {
		slog.Error("Job config validation failed", "job_id", jobConfig.JobID, "error", err)
		http.Error(w, "Validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	startPayload := queue.StartWorkflowPayload{
		Config: jobConfig,
	}

	task, err := queue.NewTask(
		queue.TypeDownloadSource,
		startPayload,
		queue.QueueNameGeneral,
	)
	if err != nil {
		slog.Error("Failed to create initial download task", "job_id", jobConfig.JobID, "error", err)
		http.Error(w, "Failed to create job task: "+err.Error(), http.StatusInternalServerError)
		return
	}

	info, err := h.queueClient.EnqueueContext(r.Context(), task, asynq.MaxRetry(0))
	if err != nil {
		slog.Error("Failed to enqueue initial download task", "job_id", jobConfig.JobID, "error", err)
		http.Error(w, "Failed to enqueue job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("Successfully enqueued new VOD workflow", "job_id", jobConfig.JobID, "queue_id", info.ID, "queue", info.Queue)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"message":  "Job workflow accepted for processing.",
		"job_id":   jobConfig.JobID,
		"queue_id": info.ID,
		"queue":    info.Queue,
	})
}

// GetJobStatus stub   status lives in central/Redis eventually.
func (h *JobHandler) GetJobStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobID := vars["jobID"]

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(w).Encode(map[string]string{
		"job_id":  jobID,
		"status":  "not_implemented",
		"message": "Job status checking is not yet available.",
	})
}
