package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/core/store"
	"github.com/muntader/zaynin-engine/internal/live/media"
)

// StreamService is the facade over redis (cluster) and Manager (this node).
type StreamService struct {
	manager     *core.Manager
	streamStore *store.StreamStore
}

func NewStreamService(m *core.Manager, ss *store.StreamStore) *StreamService {
	return &StreamService{manager: m, streamStore: ss}
}

func (s *StreamService) PrepareStream(ctx context.Context, configJSON string) (*media.StreamConfig, error) {
	var streamConfig media.StreamConfig
	if err := json.Unmarshal([]byte(configJSON), &streamConfig); err != nil {
		return nil, fmt.Errorf("failed to parse config_json: %w", err)
	}

	if streamConfig.ID == "" || streamConfig.RTMPKey == "" {
		return nil, fmt.Errorf("stream config must contain a non-empty 'id' and 'rtmp_key'")
	}

	err := s.streamStore.PrepareActiveStream(ctx, &streamConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to store prepared stream config: %w", err)
	}

	return &streamConfig, nil
}

func (s *StreamService) ListAllActiveStreams(ctx context.Context) ([]map[string]string, error) {
	ids, err := s.streamStore.GetActiveStreamIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []map[string]string{}, nil // json clients hate nil slices
	}
	return s.streamStore.GetAllStreamInfo(ctx, ids)
}

func (s *StreamService) ListRecentlyClosedStreams(ctx context.Context, count int64) ([]map[string]string, error) {
	ids, err := s.streamStore.GetRecentlyClosedStreamIDs(ctx, count)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []map[string]string{}, nil
	}
	return s.streamStore.GetAllStreamInfo(ctx, ids)
}

func (s *StreamService) GetStreamDetails(streamID string) (interface{}, error) {
	return s.manager.GetStreamDetails(streamID)
}

func (s *StreamService) StopStream(streamID string) error {
	s.manager.DeactivateStream(streamID)
	return nil
}
