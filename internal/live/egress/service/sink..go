package service

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/media"

	"github.com/muntader/zaynin-engine/internal/vod/service"
	"github.com/muntader/zaynin-engine/internal/vod/types"
)

func init() {
	egress.RegisterSink("upload_to_storage", NewLiveWatcherSink)
}

// LiveWatcherSink watches the packager output dir and ships segments upstream.
type LiveWatcherSink struct {
	streamID       string
	sessionID      string
	outputDir      string
	watcher        *LiveStreamWatcher
	storageService *service.StorageService
	outputs        []types.OutputStorage
	httpEndpoint   string
	httpAuth       *HTTPAuthConfig
	isRunning      bool
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.RWMutex
}

type HTTPAuthConfig struct {
	Token     string            `json:"token"`
	Headers   map[string]string `json:"headers"`
	StreamID  string            `json:"streamId"`
	SessionID string            `json:"sessionId"`
}

func NewLiveWatcherSink(config map[string]interface{}) (egress.Sink, error) {
	stream, ok := config["stream"].(*core.Stream)
	if !ok {
		return nil, fmt.Errorf("stream not provided or invalid type")
	}

	sessionID, ok := config["sessionID"].(string)
	if !ok {
		return nil, fmt.Errorf("sessionID not provided or invalid type")
	}

	appConfig, ok := config["appConfig"].(configTypes.Config)
	if !ok {
		return nil, fmt.Errorf("appConfig not provided or invalid type")
	}

	storageService, ok := config["storageService"].(*service.StorageService)
	if !ok {
		return nil, fmt.Errorf("storageService not provided or invalid type")
	}

	outputs, _ := config["outputs"].([]types.OutputStorage)
	httpEndpoint, _ := config["httpEndpoint"].(string)

	var httpAuth *HTTPAuthConfig
	if authConfig, exists := config["httpAuth"]; exists {
		if auth, ok := authConfig.(map[string]interface{}); ok {
			httpAuth = &HTTPAuthConfig{}
			if token, ok := auth["token"].(string); ok {
				httpAuth.Token = token
			}
			if streamId, ok := auth["streamId"].(string); ok {
				httpAuth.StreamID = streamId
			} else {
				httpAuth.StreamID = stream.ID()
			}
			if sessionId, ok := auth["sessionId"].(string); ok {
				httpAuth.SessionID = sessionId
			} else {
				httpAuth.SessionID = sessionID
			}
			if headers, ok := auth["headers"].(map[string]string); ok {
				httpAuth.Headers = headers
			}
		}
	}

	storagePath := appConfig.Storage.Paths.LiveMedia
	outputDir := filepath.Join(storagePath, "live", stream.ID(), sessionID)

	ctx, cancel := context.WithCancel(context.Background())

	sink := &LiveWatcherSink{
		streamID:       stream.ID(),
		sessionID:      sessionID,
		outputDir:      outputDir,
		storageService: storageService,
		outputs:        outputs,
		httpEndpoint:   httpEndpoint,
		httpAuth:       httpAuth,
		ctx:            ctx,
		cancel:         cancel,
	}

	return sink, nil
}

func (lws *LiveWatcherSink) Start() error {
	lws.mu.Lock()
	defer lws.mu.Unlock()

	if lws.isRunning {
		return fmt.Errorf("live watcher sink already running")
	}

	watcherConfig := WatcherConfig{
		WatchDir:        lws.outputDir,
		HTTPEndpoint:    lws.httpEndpoint,
		HTTPAuth:        lws.httpAuth,
		Outputs:         lws.outputs,
		MaxRetries:      3,
		RetryDelay:      time.Second * 2,
		WorkerCount:     5,
		CleanupInterval: time.Minute * 5,
	}

	var err error
	lws.watcher, err = NewLiveStreamWatcher(lws.storageService, watcherConfig)
	if err != nil {
		return fmt.Errorf("failed to create live stream watcher: %w", err)
	}

	if err := lws.watcher.Start(); err != nil {
		return fmt.Errorf("failed to start live stream watcher: %w", err)
	}

	lws.isRunning = true

	go lws.monitorWatcher()

	return nil
}

func (lws *LiveWatcherSink) IsRunning() bool {
	lws.mu.RLock()
	defer lws.mu.RUnlock()
	return lws.isRunning
}

func (lws *LiveWatcherSink) GetStats() map[string]interface{} {
	lws.mu.RLock()
	defer lws.mu.RUnlock()

	stats := map[string]interface{}{
		"streamID":     lws.streamID,
		"sessionID":    lws.sessionID,
		"outputDir":    lws.outputDir,
		"isRunning":    lws.isRunning,
		"httpEndpoint": lws.httpEndpoint,
		"hasAuth":      lws.httpAuth != nil,
		"outputCount":  len(lws.outputs),
	}

	if lws.watcher != nil {
		watcherStats := lws.watcher.GetStats()
		for k, v := range watcherStats {
			stats[k] = v
		}
	}

	return stats
}

func (lws *LiveWatcherSink) monitorWatcher() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-lws.ctx.Done():
			return
		case <-ticker.C:
			if lws.watcher != nil {
				stats := lws.watcher.GetStats()
				slog.Debug("live watcher stats", "stream", lws.streamID, "stats", stats)
			}
		}
	}
}

func (lws *LiveWatcherSink) ID() string {
	return lws.streamID
}

func (lws *LiveWatcherSink) Close() error {
	lws.mu.Lock()
	defer lws.mu.Unlock()

	if !lws.isRunning {
		return nil
	}

	lws.cancel()

	if lws.watcher != nil {
		lws.watcher.Stop()
	}

	lws.isRunning = false

	return nil
}

// WritePacket is a no-op   we watch the filesystem instead.
func (lws *LiveWatcherSink) WritePacket(packet *media.Packet) error {
	return nil
}
