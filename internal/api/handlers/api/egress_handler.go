package api

import (
	"errors"
	"github.com/gorilla/mux"
	"github.com/muntader/zaynin-engine/internal/api/helpers"
	"github.com/muntader/zaynin-engine/internal/live/core/service"
	"github.com/muntader/zaynin-engine/internal/live/core/store"
	"net/http"
)

type EgressHandler struct {
	egressSvc *service.EgressService
}

func NewEgressHandler(es *service.EgressService) *EgressHandler {
	return &EgressHandler{egressSvc: es}
}

func (h *EgressHandler) AddRtmpPushSink(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	streamID := vars["streamID"]

	var req service.AddRtmpPushSinkRequest
	if err := helpers.DecodeAndValidate(r, &req); err != nil {
		helpers.WriteAPIError(w, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}

	egressID, err := h.egressSvc.AddRtmpPushSink(r.Context(), streamID, req)
	if err != nil {
		if errors.Is(err, store.ErrEgressAlreadyExists) {
			helpers.WriteAPIError(w, http.StatusConflict, "Egress already exists", err.Error())
			return
		}
		helpers.WriteAPIError(w, http.StatusInternalServerError, "Failed to create sink", err.Error())
		return
	}

	response := map[string]string{"message": "Command to start sink accepted.", "egressId": egressID}
	helpers.WriteJSON(w, http.StatusAccepted, response)
}

func (h *EgressHandler) StopSink(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	streamID, egressID := vars["streamID"], vars["egressID"]

	if err := h.egressSvc.StopSink(r.Context(), streamID, egressID); err != nil {
		if errors.Is(err, store.ErrEgressNotFound) {
			helpers.WriteAPIError(w, http.StatusNotFound, "Egress not found", err.Error())
			return
		}
		helpers.WriteAPIError(w, http.StatusInternalServerError, "Failed to stop sink", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *EgressHandler) GetConfiguredSinksForStream(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	streamID := vars["streamID"]

	sinks, err := h.egressSvc.GetConfiguredSinksForStream(r.Context(), streamID)
	if err != nil {
		helpers.WriteAPIError(w, http.StatusInternalServerError, "Failed to retrieve sinks", err.Error())
		return
	}
	helpers.WriteJSON(w, http.StatusOK, sinks)
}
