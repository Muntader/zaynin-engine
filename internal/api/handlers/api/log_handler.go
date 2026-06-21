package api

import (
	"encoding/json"
	"github.com/muntader/zaynin-engine/internal/common/logging"
	"net/http"
	"strconv"
)

type LogHandler struct {
	store *logging.LogStore
}

func NewLogHandler(store *logging.LogStore) *LogHandler {
	return &LogHandler{store: store}
}

// GetLogs reads the Redis-backed log ring with optional level filter + pagination.
func (h *LogHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	level := query.Get("level")

	offset, err := strconv.ParseInt(query.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		offset = 0
	}

	limit, err := strconv.ParseInt(query.Get("limit"), 10, 64)
	if err != nil || limit <= 0 {
		limit = 50
	}

	logs, err := h.store.GetLogs(r.Context(), level, offset, limit)
	if err != nil {
		http.Error(w, "Failed to retrieve logs from the store.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(logs); err != nil {
		http.Error(w, "Failed to encode logs to JSON.", http.StatusInternalServerError)
	}
}
