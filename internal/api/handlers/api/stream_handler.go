package api

import (
	"github.com/gorilla/mux"
	"github.com/muntader/zaynin-engine/internal/api/helpers"
	"github.com/muntader/zaynin-engine/internal/live/core/service"
	"net/http"
	"strconv"
)

type StreamHandler struct {
	streamSvc *service.StreamService
	egressSvc *service.EgressService
}

func NewStreamHandler(streamSvc *service.StreamService, egressSvc *service.EgressService) *StreamHandler {
	return &StreamHandler{streamSvc: streamSvc, egressSvc: egressSvc}
}

func (h *StreamHandler) ListAllActiveStreams(w http.ResponseWriter, r *http.Request) {
	streams, err := h.streamSvc.ListAllActiveStreams(r.Context())
	if err != nil {
		helpers.WriteAPIError(w, http.StatusInternalServerError, "Failed to get active streams from cluster", err.Error())
		return
	}
	helpers.WriteJSON(w, http.StatusOK, streams)
}

func (h *StreamHandler) ListRecentlyClosedStreams(w http.ResponseWriter, r *http.Request) {
	countStr := r.URL.Query().Get("count")
	count, err := strconv.ParseInt(countStr, 10, 64)
	if err != nil || count <= 0 {
		count = 20
	}

	streams, err := h.streamSvc.ListRecentlyClosedStreams(r.Context(), count)
	if err != nil {
		helpers.WriteAPIError(w, http.StatusInternalServerError, "Failed to get recently closed streams from cluster", err.Error())
		return
	}
	helpers.WriteJSON(w, http.StatusOK, streams)
}

func (h *StreamHandler) GetStreamDetails(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	streamID := vars["streamID"]

	details, err := h.egressSvc.GetStreamDetailsWithSinks(r.Context(), streamID)
	if err != nil {
		helpers.WriteAPIError(w, http.StatusNotFound, "Failed to get stream details", err.Error())
		return
	}
	helpers.WriteJSON(w, http.StatusOK, details)
}

func (h *StreamHandler) GetNodeStreamDetails(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	streamID := vars["streamID"]

	details, err := h.streamSvc.GetStreamDetails(streamID)
	if err != nil {
		helpers.WriteAPIError(w, http.StatusNotFound, err.Error(), nil)
		return
	}
	helpers.WriteJSON(w, http.StatusOK, details)
}

func (h *StreamHandler) ForceStopStream(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	streamID := vars["streamID"]

	if err := h.streamSvc.StopStream(streamID); err != nil {
		helpers.WriteAPIError(w, http.StatusNotFound, err.Error(), nil)
		return
	}
	helpers.WriteJSON(w, http.StatusAccepted, map[string]string{"message": "Stream termination initiated on this node."})
}
