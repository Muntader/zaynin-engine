package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/core/store"
)

// EgressService handles dynamic sink attach/detach across the cluster.
type EgressService struct {
	manager     *core.Manager      // node-local live state
	egressStore *store.EgressStore // cluster-wide sink configs
	streamStore *store.StreamStore // for combined detail endpoints
}

func NewEgressService(m *core.Manager, es *store.EgressStore, ss *store.StreamStore) *EgressService {
	return &EgressService{manager: m, egressStore: es, streamStore: ss}
}

type AddRtmpPushSinkRequest struct {
	Platform  string `json:"platform" validate:"required,alphanum"`
	RemoteURL string `json:"remote_url" validate:"required,url"`
	APIKey    string `json:"api_key" validate:"required"`
}

type StreamDetailsWithSinks struct {
	Info  map[string]string   `json:"info"`
	Sinks []*store.EgressInfo `json:"sinks"`
}

// AddRtmpPushSink upserts config then always fires a start   reconnect-safe.
func (s *EgressService) AddRtmpPushSink(ctx context.Context, streamID string, req AddRtmpPushSinkRequest) (string, error) {
	const sinkType = "rtmp_push"
	egressID := store.GenerateEgressID(streamID, sinkType, req.Platform)

	info := &store.EgressInfo{
		ID:       egressID,
		StreamID: streamID,
		Type:     sinkType,
		Settings: map[string]interface{}{
			"id":        egressID,
			"remoteURL": req.RemoteURL,
			"apiKey":    req.APIKey,
			"platform":  req.Platform,
		},
	}

	err := s.egressStore.CreateEgress(ctx, info)

	if err != nil {
		// already exists is fine   we still want to poke the worker
		if errors.Is(err, store.ErrEgressAlreadyExists) {
			slog.Warn("Configuration for egress already exists. Proceeding to send start command.", "egress_id", egressID)
		} else {
			return "", fmt.Errorf("failed to store egress configuration: %w", err)
		}
	} else {
		slog.Info("Successfully created new egress configuration", "egress_id", egressID)
	}

	// start command wakes the sink even when config was already there
	event := store.EgressControlEvent{
		Action:   store.EgressActionStart,
		StreamID: streamID,
		EgressID: egressID,
		Config:   &store.EgressConfig{Type: sinkType, Settings: info.Settings},
	}

	if err := s.egressStore.NotifyEgressUpdate(ctx, event); err != nil {
		slog.Error("Failed to publish start_egress event", "egress_id", egressID, "error", err)
		return "", fmt.Errorf("failed to publish start_egress event: %w", err)
	}

	slog.Info("Service: successfully requested start for sink", "egress_id", egressID)
	return egressID, nil
}

// StopSink tells the worker to drop the sink and cleans up redis.
func (s *EgressService) StopSink(ctx context.Context, streamID, egressID string) error {
	slog.Info("Service: preparing 'stop' command for sink on stream", "egress_id", egressID, "stream_id", streamID)
	const sinkType = "rtmp_push"

	info, err := s.egressStore.GetEgress(ctx, egressID)
	if err != nil {
		return err
	}

	if info.StreamID != streamID {
		return fmt.Errorf("egress '%s' does not belong to stream '%s'", egressID, streamID)
	}

	event := store.EgressControlEvent{Action: store.EgressActionStop, StreamID: streamID, EgressID: egressID,
		Config: &store.EgressConfig{Type: sinkType, Settings: info.Settings},
	}
	if err := s.egressStore.NotifyEgressUpdate(ctx, event); err != nil {
		return fmt.Errorf("failed to publish stop_egress event: %w", err)
	}

	if err := s.egressStore.DeleteEgress(ctx, streamID, egressID); err != nil {
		slog.Error("Stop command was sent, but failed to delete Redis record", "egress_id", egressID, "error", err)
		return fmt.Errorf("failed to delete egress record: %w", err)
	}

	return nil
}

func (s *EgressService) GetConfiguredSinksForStream(ctx context.Context, streamID string) ([]*store.EgressInfo, error) {
	return s.egressStore.ListEgressesForStream(ctx, streamID)
}

func (s *EgressService) GetStreamDetailsWithSinks(ctx context.Context, streamID string) (*StreamDetailsWithSinks, error) {
	streamInfo, err := s.streamStore.GetStreamInfo(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream info for '%s': %w", streamID, err)
	}
	if streamInfo == nil {
		return nil, fmt.Errorf("stream with id '%s' not found", streamID)
	}

	sinks, err := s.egressStore.ListEgressesForStream(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("failed to list egresses for stream '%s': %w", streamID, err)
	}

	response := &StreamDetailsWithSinks{
		Info:  streamInfo,
		Sinks: sinks,
	}
	return response, nil
}
