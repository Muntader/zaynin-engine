package queue

import (
	"github.com/muntader/zaynin-engine/internal/vod/service"
)

// VODRecordingHandler handles live-to-VOD harvest tasks (legacy wrapper).
type VODRecordingHandler struct {
	StorageService *service.StorageService
}

func NewVODRecordingHandler(ss *service.StorageService) *VODRecordingHandler {
	return &VODRecordingHandler{StorageService: ss}
}
