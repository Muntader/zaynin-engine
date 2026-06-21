package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	RedisLogKey  = "app:logs"
	LogRetention = 7 * 24 * time.Hour
)

// RedisHandler writes slog records into a Redis sorted set for the log API.
type RedisHandler struct {
	client *redis.Client
	opts   slog.HandlerOptions
	key    string
}

func NewRedisHandler(client *redis.Client, opts *slog.HandlerOptions) *RedisHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	return &RedisHandler{
		client: client,
		opts:   *opts,
		key:    RedisLogKey,
	}
}

func (h *RedisHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *RedisHandler) Handle(_ context.Context, r slog.Record) error {
	logData := make(map[string]interface{})
	logData["time"] = r.Time.UTC().Format(time.RFC3339Nano)
	logData["level"] = r.Level.String()
	logData["msg"] = r.Message

	r.Attrs(func(a slog.Attr) bool {
		logData[a.Key] = a.Value.Any()
		return true
	})

	jsonBytes, err := json.Marshal(logData)
	if err != nil {
		return err
	}

	member := redis.Z{
		Score:  float64(r.Time.UnixNano()),
		Member: jsonBytes,
	}

	ctx := context.Background()
	pipe := h.client.Pipeline()
	pipe.ZAdd(ctx, h.key, member)

	maxTimestamp := float64(time.Now().Add(-LogRetention).UnixNano())
	pipe.ZRemRangeByScore(ctx, h.key, "-inf", strconv.FormatFloat(maxTimestamp, 'f', -1, 64))

	_, err = pipe.Exec(ctx)
	return err
}

func (h *RedisHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *RedisHandler) WithGroup(name string) slog.Handler {
	return h
}
