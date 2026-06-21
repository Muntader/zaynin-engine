package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/muntader/zaynin-engine/internal/live/media"

	"github.com/redis/go-redis/v9"
)

const (
	activeStreamsSetKey     = "streams:active"
	closedStreamsZSetKey    = "streams:closed"
	streamInfoHashKeyPrefix = "stream:info:"
	closedStreamTTL         = 24 * time.Hour
	preparedStreamTTL       = 60 * time.Second
)

// StreamStore is redis-backed cluster state for streams.
type StreamStore struct {
	client *redis.Client
	nodeID string
}

func NewStreamStore(client *redis.Client, nodeID string) *StreamStore {
	return &StreamStore{client: client, nodeID: nodeID}
}

func (st *StreamStore) PrepareActiveStream(ctx context.Context, streamConfig *media.StreamConfig) error {
	// lookup key might differ from the canonical stream id
	streamID := streamConfig.RTMPKey
	streamKey := streamInfoHashKeyPrefix + streamID

	// stash the full config   workers read this back at publish time
	configJSON, err := json.Marshal(streamConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal stream config: %w", err)
	}

	streamData := map[string]interface{}{
		"id":         streamID,
		"rtmpKey":    streamConfig.RTMPKey,
		"nodeId":     st.nodeID,
		"preparedAt": time.Now().UTC().Format(time.RFC3339),
		"pipeline":   streamConfig.Pipeline.Name,
		"status":     "prepared",
		"configJSON": string(configJSON),
	}

	pipe := st.client.Pipeline()
	pipe.HSet(ctx, streamKey, streamData)
	// prepared streams expire if nobody publishes in time
	pipe.Expire(ctx, streamKey, preparedStreamTTL)

	_, err = pipe.Exec(ctx)
	return err
}

func (st *StreamStore) AddActiveStream(ctx context.Context, streamID string) error {
	streamKey := streamInfoHashKeyPrefix + streamID

	streamData := map[string]interface{}{
		"status":    "active",
		"startedAt": time.Now().UTC().Format(time.RFC3339),
	}

	pipe := st.client.Pipeline()
	pipe.HSet(ctx, streamKey, streamData)
	pipe.Persist(ctx, streamKey) // live streams shouldnt TTL out mid-broadcast
	pipe.SAdd(ctx, activeStreamsSetKey, streamID)

	_, err := pipe.Exec(ctx)
	return err
}

func (st *StreamStore) RemoveActiveStream(ctx context.Context, streamID string) error {
	pipe := st.client.Pipeline()
	pipe.SRem(ctx, activeStreamsSetKey, streamID)
	timestamp := float64(time.Now().Unix())
	pipe.ZAdd(ctx, closedStreamsZSetKey, redis.Z{Score: timestamp, Member: streamID})
	streamKey := streamInfoHashKeyPrefix + streamID
	pipe.HSet(ctx, streamKey, "status", "closed", "stoppedAt", time.Now().UTC().Format(time.RFC3339))
	pipe.Expire(ctx, streamKey, closedStreamTTL)
	maxTimestamp := float64(time.Now().Add(-closedStreamTTL).Unix())
	pipe.ZRemRangeByScore(ctx, closedStreamsZSetKey, "-inf", fmt.Sprintf("(%f", maxTimestamp))
	_, err := pipe.Exec(ctx)
	return err
}

func (st *StreamStore) GetActiveStreamIDs(ctx context.Context) ([]string, error) {
	return st.client.SMembers(ctx, activeStreamsSetKey).Result()
}

func (st *StreamStore) GetRecentlyClosedStreamIDs(ctx context.Context, count int64) ([]string, error) {
	return st.client.ZRevRange(ctx, closedStreamsZSetKey, 0, count-1).Result()
}

// GetStreamInfo returns nil,nil when the key is missing   thats not an error.
func (st *StreamStore) GetStreamInfo(ctx context.Context, streamID string) (map[string]string, error) {
	key := streamInfoHashKeyPrefix + streamID
	result, err := st.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get info for stream '%s': %w", streamID, err)
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

func (st *StreamStore) GetAllStreamInfo(ctx context.Context, streamIDs []string) ([]map[string]string, error) {
	if len(streamIDs) == 0 {
		return []map[string]string{}, nil
	}
	pipe := st.client.Pipeline()
	cmds := make(map[string]*redis.MapStringStringCmd)
	for _, id := range streamIDs {
		cmds[id] = pipe.HGetAll(ctx, streamInfoHashKeyPrefix+id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	results := make([]map[string]string, 0, len(streamIDs))
	for _, id := range streamIDs {
		// pipeline might return empty for deleted keys
		if info := cmds[id].Val(); len(info) > 0 {
			results = append(results, info)
		}
	}
	return results, nil
}

func (st *StreamStore) CleanUpNodeStreams(ctx context.Context) error {
	activeIDs, err := st.GetActiveStreamIDs(ctx)
	if err != nil {
		return fmt.Errorf("could not get active streams for cleanup: %w", err)
	}
	if len(activeIDs) == 0 {
		slog.Info("Node cleanup complete. No orphaned streams found.")
		return nil
	}

	infos, err := st.GetAllStreamInfo(ctx, activeIDs)
	if err != nil {
		return fmt.Errorf("could not get stream info for cleanup: %w", err)
	}

	cleanupCount := 0
	for _, info := range infos {
		if node, ok := info["nodeId"]; ok && node == st.nodeID {
			streamID := info["id"]
			slog.Warn("Found orphaned stream from previous session. Marking as closed.", "streamID", streamID)
			if err := st.RemoveActiveStream(ctx, streamID); err != nil {
				slog.Error("Failed to clean up orphaned stream", "streamID", streamID, "error", err)
			} else {
				cleanupCount++
			}
		}
	}
	return nil
}
