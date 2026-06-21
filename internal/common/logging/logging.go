package logging

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/redis/go-redis/v9"
)

// Init configures the global slog logger (console + optional Redis sink).
func Init(level, format string, redisClient *redis.Client) {
	var logLevel slog.Level
	err := logLevel.UnmarshalText([]byte(level))
	if err != nil {
		logLevel = slog.LevelInfo
	}

	var consoleHandler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		consoleHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	case "text":
		fallthrough
	default:
		consoleHandler = tint.NewHandler(os.Stdout, &tint.Options{Level: logLevel, TimeFormat: time.Kitchen})
	}

	var finalHandler slog.Handler = consoleHandler
	if redisClient != nil {
		redisHandler := NewRedisHandler(redisClient, &slog.HandlerOptions{Level: slog.LevelDebug})
		finalHandler = MultiHandler(consoleHandler, redisHandler)
		slog.Info("Logger initialized with Redis backend.")
	}

	logger := slog.New(finalHandler)
	slog.SetDefault(logger)
}

func Mute() {
}

// SanitizeKey avoids empty attribute keys breaking JSON log consumers.
func SanitizeKey(key string) string {
	if key == "" {
		return "empty_key"
	}
	return key
}
