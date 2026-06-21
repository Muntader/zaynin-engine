package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
)

type LogStore struct {
	client *redis.Client
	ctx    context.Context
}

func NewLogStore(client *redis.Client) *LogStore {
	return &LogStore{
		client: client,
		ctx:    context.Background(),
	}
}

type LogEntry map[string]interface{}

// GetLogs returns newest-first entries; level filter is applied after fetch.
func (s *LogStore) GetLogs(ctx context.Context, level string, offset, limit int64) ([]LogEntry, error) {
	logStrings, err := s.client.ZRevRange(ctx, RedisLogKey, offset, offset+limit-1).Result()
	if err != nil {
		return nil, err
	}

	var logs []LogEntry
	for _, logStr := range logStrings {
		var entry LogEntry
		if err := json.Unmarshal([]byte(logStr), &entry); err != nil {
			slog.Warn("Failed to unmarshal log entry from Redis", "error", err)
			continue
		}

		if level != "" {
			if entryLevel, ok := entry["level"].(string); ok {
				if strings.EqualFold(entryLevel, level) {
					logs = append(logs, entry)
				}
			}
		} else {
			logs = append(logs, entry)
		}
	}

	return logs, nil
}
