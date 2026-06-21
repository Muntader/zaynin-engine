package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	EgressControlChannel = "egress:control"
	EgressActionStart    = "start"
	EgressActionStop     = "stop"
)

const (
	egressInfoKeyPrefix     = "egress:info:"
	streamEgressesKeyPrefix = "stream:egresses:"
)

var (
	ErrEgressNotFound      = errors.New("egress not found")
	ErrEgressAlreadyExists = errors.New("egress with this configuration already exists")
)

// EgressInfo is the persisted sink config   workers subscribe to changes.
type EgressInfo struct {
	ID           string                 `json:"id" redis:"id"`
	StreamID     string                 `json:"streamId" redis:"streamId"`
	Type         string                 `json:"type" redis:"type"`
	Settings     map[string]interface{} `json:"settings" redis:"-"`
	SettingsJSON string                 `json:"-" redis:"settings"`
}

// EgressConfig rides along on start events.
type EgressConfig struct {
	Type     string                 `json:"type"`
	Settings map[string]interface{} `json:"settings"`
}

type EgressControlEvent struct {
	Action   string        `json:"action"`
	StreamID string        `json:"streamId"`
	EgressID string        `json:"egressId"`
	Config   *EgressConfig `json:"config,omitempty"`
}

// EgressStore keeps sink configs in redis and broadcasts control commands.
type EgressStore struct {
	redisClient *redis.Client
}

func NewEgressStore(redisClient *redis.Client) *EgressStore {
	return &EgressStore{redisClient: redisClient}
}

func egressInfoKey(egressID string) string {
	return egressInfoKeyPrefix + egressID
}

func streamEgressesKey(streamID string) string {
	return streamEgressesKeyPrefix + streamID
}

// CreateEgress fails if the id already exists   callers can treat that as idempotent.
func (es *EgressStore) CreateEgress(ctx context.Context, info *EgressInfo) error {
	settingsJSON, err := json.Marshal(info.Settings)
	if err != nil {
		return fmt.Errorf("failed to marshal egress settings: %w", err)
	}
	info.SettingsJSON = string(settingsJSON)

	key := egressInfoKey(info.ID)

	pipe := es.redisClient.TxPipeline()

	// HSetNX is our "create only" guard
	setResult := pipe.HSetNX(ctx, key, "id", info.ID)

	pipe.HSet(ctx, key, "streamId", info.StreamID)
	pipe.HSet(ctx, key, "type", info.Type)
	pipe.HSet(ctx, key, "settings", info.SettingsJSON)

	pipe.SAdd(ctx, streamEgressesKey(info.StreamID), info.ID)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis transaction failed for creating egress: %w", err)
	}

	if !setResult.Val() {
		return ErrEgressAlreadyExists
	}

	return nil
}

func (es *EgressStore) DeleteEgress(ctx context.Context, streamID, egressID string) error {
	pipe := es.redisClient.TxPipeline()
	pipe.Del(ctx, egressInfoKey(egressID))
	pipe.SRem(ctx, streamEgressesKey(streamID), egressID)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis transaction failed for deleting egress: %w", err)
	}
	return nil
}

func (es *EgressStore) GetEgress(ctx context.Context, egressID string) (*EgressInfo, error) {
	result, err := es.redisClient.HGetAll(ctx, egressInfoKey(egressID)).Result()
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, ErrEgressNotFound
	}

	info := &EgressInfo{
		ID:           result["id"],
		StreamID:     result["streamId"],
		Type:         result["type"],
		SettingsJSON: result["settings"],
	}

	if err := json.Unmarshal([]byte(info.SettingsJSON), &info.Settings); err != nil {
		return nil, fmt.Errorf("failed to unmarshal egress settings: %w", err)
	}

	return info, nil
}

func (es *EgressStore) ListEgressesForStream(ctx context.Context, streamID string) ([]*EgressInfo, error) {
	egressIDs, err := es.redisClient.SMembers(ctx, streamEgressesKey(streamID)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get egress IDs for stream %s: %w", streamID, err)
	}

	if len(egressIDs) == 0 {
		return []*EgressInfo{}, nil
	}

	infos := make([]*EgressInfo, 0, len(egressIDs))
	for _, id := range egressIDs {
		info, err := es.GetEgress(ctx, id)
		if err != nil {
			// stale index entry   log and move on
			slog.Warn("Could not retrieve details for egress ID", "id", id, "error", err)
			continue
		}
		infos = append(infos, info)
	}

	return infos, nil
}

// GenerateEgressID is deterministic so reconnects hit the same redis key.
func GenerateEgressID(streamID, sinkType, sinkIdentifier string) string {
	safeIdentifier := strings.NewReplacer(":", "-", " ", "_").Replace(sinkIdentifier)
	return fmt.Sprintf("egress:%s:%s:%s", streamID, sinkType, safeIdentifier)
}

func (es *EgressStore) NotifyEgressUpdate(ctx context.Context, event EgressControlEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal egress control event: %w", err)
	}
	return es.redisClient.Publish(ctx, EgressControlChannel, payload).Err()
}

func (es *EgressStore) SubscribeToEgressUpdates(ctx context.Context, handler func(event EgressControlEvent)) {
	pubsub := es.redisClient.Subscribe(ctx, EgressControlChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for msg := range ch {
		var event EgressControlEvent
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			slog.Error("Could not unmarshal egress control event", "error", err)
			continue
		}
		handler(event)
	}
}
