package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"github.com/muntader/zaynin-engine/internal/vod/queue"

	"github.com/google/uuid"
	"github.com/muntader/zaynin-engine/internal/common/notifier"
	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/common/utils"
	"github.com/muntader/zaynin-engine/internal/hardware"
	"github.com/muntader/zaynin-engine/internal/live/core/store"
	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/ingress"
	"github.com/muntader/zaynin-engine/internal/live/media"
	"github.com/redis/go-redis/v9"
)

// StreamDetails is what the API returns when someone asks "what's running on this stream?"
type StreamDetails struct {
	ID         string        `json:"id"`
	Uptime     string        `json:"uptime"`
	Properties interface{}   `json:"properties"`
	Sinks      []interface{} `json:"sinks"`
}

// Manager tracks everything live on one worker node.
type Manager struct {
	// separate locks so reads dont block cleanup
	streamsMutex  sync.RWMutex
	activeStreams map[string]*Stream

	cleanupMutex sync.Mutex
	cleanupWg    sync.WaitGroup

	// cheap count for health checks without grabbing the map lock
	streamCount int64

	nodeID          string
	AppConfig       configTypes.Config
	Notifier        *notifier.Notifier
	PortManager     *utils.PortManager
	StreamStore     *store.StreamStore
	EgressStore     *store.EgressStore
	ResourceManager *hardware.ResourceManager
	QueueClient     *asynq.Client
}

// StatusReporter lets sinks expose richer status when they have it.
type StatusReporter interface {
	GetStatus() interface{}
}

// NewManager boots the node   cleans up stale streams from last crash too.
func NewManager(cfg configTypes.Config, redisClient *redis.Client, commonNotifier *notifier.Notifier, resourceManager *hardware.ResourceManager, queueClient *asynq.Client) (*Manager, error) {
	nodeID, err := os.Hostname()
	if err != nil {
		slog.Warn("Could not get hostname, falling back to UUID", "error", err)
		nodeID = uuid.New().String()
	}

	streamStore := store.NewStreamStore(redisClient, nodeID)
	if err := streamStore.CleanUpNodeStreams(context.Background()); err != nil {
		slog.Error("Error during initial node stream cleanup", "error", err)
	}

	egressStore := store.NewEgressStore(redisClient)

	manager := &Manager{
		activeStreams:   make(map[string]*Stream),
		nodeID:          nodeID,
		AppConfig:       cfg,
		Notifier:        commonNotifier,
		PortManager:     utils.NewPortManager(cfg.Resources.DynamicPortRange.Start, cfg.Resources.DynamicPortRange.End),
		StreamStore:     streamStore,
		EgressStore:     egressStore,
		ResourceManager: resourceManager,
		QueueClient:     queueClient,
	}

	go manager.EgressStore.SubscribeToEgressUpdates(context.Background(), manager.handleEgressControlEvent)

	return manager, nil
}

// StartStream validates against redis, then hands off to a supervisor goroutine.
func (m *Manager) StartStream(source ingress.Source) (*Stream, error) {
	streamID := source.ID()
	ctx := context.Background()

	fmt.Println("Starting stream", streamID)
	// read lock is enough here   we're just checking
	if _, exists := m.GetActiveStream(streamID); exists {
		return nil, fmt.Errorf("stream with id '%s' is already active on this node", streamID)
	}

	// redis is the gatekeeper   no prepared config means no stream
	streamInfo, err := m.StreamStore.GetStreamInfo(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("internal error checking stream store: %w", err)
	}

	if streamInfo == nil {
		return nil, fmt.Errorf("stream key '%s' is invalid or has expired", streamID)
	}

	status, _ := streamInfo["status"]
	if status != "prepared" {
		return nil, fmt.Errorf("stream '%s' is not in a prepared state (current status: %s)",
			streamID, status)
	}

	configJSON, _ := streamInfo["configJSON"]
	var streamConfig media.StreamConfig
	if err := json.Unmarshal([]byte(configJSON), &streamConfig); err != nil {
		return nil, fmt.Errorf("failed to parse prepared config: %w", err)
	}

	stream := NewStream(source, &streamConfig)

	m.addActiveStream(streamID, stream)

	// cluster needs to know we're live before we actually start pumping
	if err := m.StreamStore.AddActiveStream(ctx, streamID); err != nil {
		m.removeActiveStream(streamID) // roll back in-memory state
		stream.Stop()
		return nil, fmt.Errorf("CRITICAL: failed to activate stream in store: %w", err)
	}

	m.cleanupWg.Add(1)
	go m.superviseStream(stream)

	return stream, nil
}

// DeactivateStream is fire-and-forget   caller shouldn't block on teardown.
func (m *Manager) DeactivateStream(streamID string) {
	stream, ok := m.GetActiveStream(streamID)
	if !ok {
		return
	}

	// drop from map first so new requests dont see a dying stream
	m.removeActiveStream(streamID)

	stream.Stop()

}

// GetStreamDetails builds the status blob StreamService serves to the API.
func (m *Manager) GetStreamDetails(streamID string) (interface{}, error) {
	stream, ok := m.GetActiveStream(streamID)
	if !ok {
		return nil, fmt.Errorf("stream with id '%s' not active on this node", streamID)
	}

	sinks, err := m.getAllSinkStatusesForStream(stream)
	if err != nil {
		return nil, fmt.Errorf("could not get sink statuses: %w", err)
	}

	details := StreamDetails{
		ID:         stream.ID(),
		Uptime:     time.Since(stream.startTime).Round(time.Second).String(),
		Properties: stream.Config(),
		Sinks:      sinks,
	}

	return details, nil
}

// GetAllSinkStatusesForStream returns the statuses of all sinks for a given stream.
func (m *Manager) GetAllSinkStatusesForStream(streamID string) ([]interface{}, error) {
	stream, ok := m.GetActiveStream(streamID)
	if !ok {
		return nil, fmt.Errorf("stream with id '%s' not active on this node", streamID)
	}

	sinks := stream.GetAllSinks()

	if len(sinks) == 0 {
		return []interface{}{}, nil // empty slice plays nicer with json than nil
	}

	statuses := make([]interface{}, 0, len(sinks))

	for _, sink := range sinks {
		if reporter, ok := sink.(StatusReporter); ok {
			statuses = append(statuses, reporter.GetStatus())
		} else {
			// still list it so the API doesnt look like sinks vanished
			defaultStatus := map[string]string{
				"id":   sink.ID(),
				"type": "unknown",
			}
			statuses = append(statuses, defaultStatus)
		}
	}

	return statuses, nil
}

func (m *Manager) GetActiveStream(streamID string) (*Stream, bool) {
	m.streamsMutex.RLock()
	s, ok := m.activeStreams[streamID]
	m.streamsMutex.RUnlock()
	return s, ok
}

func (m *Manager) GetActiveStreamCount() int {
	return int(atomic.LoadInt64(&m.streamCount))
}

func (m *Manager) NodeID() string {
	return m.nodeID
}

// WaitForCleanup blocks shutdown until supervisors finish tearing down.
func (m *Manager) WaitForCleanup() {
	m.cleanupWg.Wait()
}

// superviseStream owns one stream end-to-end   setup, run, cleanup.
func (m *Manager) superviseStream(stream *Stream) {
	defer m.cleanupWg.Done()

	streamID := stream.ID()
	streamConfig := stream.Config()
	sessionID := fmt.Sprintf("%d", time.Now().Unix())

	var abrPorts []int
	defer func() {
		// DVR on? kick off vod packaging so segments dont just rot on disk
		pkgCfg := streamConfig.Pipeline.Package
		if pkgCfg.DvrEnabled && len(pkgCfg.Outputs) > 0 {

			sourceDir := filepath.Join(m.AppConfig.Storage.Paths.LiveMedia, streamID, sessionID)

			payload := queue.VODRecordingPayload{
				StreamID:   streamID,
				SessionID:  sessionID,
				SourcePath: sourceDir,
				Outputs:    pkgCfg.Outputs,
			}

			task, err := queue.NewTask(queue.TypeVodRecording, payload, queue.QueueNameGeneral)
			if err != nil {
				slog.Error("Failed to create VOD finalization task", "streamID", streamID, "error", err)
			} else {
				_, err := m.QueueClient.Enqueue(task)
				if err != nil {
					slog.Error("Failed to enqueue VOD finalization task", "streamID", streamID, "error", err)
				} else {
					//slog.Info("Successfully enqueued VOD finalization task.", "streamID", streamID)
				}
			}
		}

		stream.closeAllSinks()

		if abrPorts != nil {
			m.PortManager.Release(abrPorts)
		}

		if err := m.StreamStore.RemoveActiveStream(context.Background(), streamID); err != nil {
			slog.Error("Failed to update stream status in Redis store",
				"streamID", streamID, "error", err)
		}

	}()

	// cant size the pipeline until we know what the source actually has
	err := stream.Properties().Wait(15 * time.Second)
	if err != nil {
		slog.Error("Failed to receive stream properties in time", "streamID", streamID, "error", err)
		stream.Stop()
		return
	}
	props := stream.Properties()

	// drop audio tracks the source doesnt actually have
	var validAudioRenditions []media.AudioRenditionConfig
	detectedAudioTracks := props.NumAudioTracks()
	for _, requestedAudio := range streamConfig.Pipeline.Transcode.AudioRenditions {
		if requestedAudio.InputTrackIndex < detectedAudioTracks {
			validAudioRenditions = append(validAudioRenditions, requestedAudio)
		} else {
			slog.Warn("Filtering out requested audio track that does not exist in source.", "streamID", streamID, "requested_index", requestedAudio.InputTrackIndex, "detected_tracks", detectedAudioTracks)
		}
	}
	streamConfig.Pipeline.Transcode.AudioRenditions = validAudioRenditions

	var useDemuxedPipeline bool
	if stream.Source().Format() == "flv" { // rtmp lands as flv
		useDemuxedPipeline = false
		// rtmp only carries one audio track no matter what the config says
		if len(streamConfig.Pipeline.Transcode.AudioRenditions) > 1 {
			slog.Warn("RTMP stream can only process one audio track. Using the first valid rendition.", "streamID", streamID)
			streamConfig.Pipeline.Transcode.AudioRenditions = streamConfig.Pipeline.Transcode.AudioRenditions[:1]
		}
	} else { // srt etc can do per-track outputs
		useDemuxedPipeline = true
	}

	numVideoRenditions := len(streamConfig.Pipeline.Transcode.VideoRenditions)
	numAudioRenditions := len(streamConfig.Pipeline.Transcode.AudioRenditions)

	var totalRequiredPorts int
	if useDemuxedPipeline {
		totalRequiredPorts = numVideoRenditions + numAudioRenditions
	} else {
		totalRequiredPorts = numVideoRenditions
	}

	abrPorts, err = m.PortManager.Allocate(totalRequiredPorts)
	if err != nil {
		slog.Error("Failed to allocate required ports", "streamID", streamID, "error", err)
		stream.Stop()
		return
	}

	if streamConfig.Pipeline.Transcode.Enabled {
		if streamConfig.Pipeline.Package.Enabled {
			packagerConfig := map[string]interface{}{
				"stream":         stream,
				"sessionID":      sessionID,
				"inputPorts":     abrPorts,
				"appConfig":      m.AppConfig,
				"isDemuxedInput": useDemuxedPipeline,
			}
			packager, _ := egress.NewSupervisedSink("shaka_packager", packagerConfig)
			stream.AddSink(packager)
		}
	}

	transcoderConfig := map[string]interface{}{
		"stream":           stream,
		"sessionID":        sessionID,
		"resourceManager":  m.ResourceManager,
		"outputPorts":      abrPorts,
		"appConfig":        m.AppConfig,
		"useDemuxedOutput": useDemuxedPipeline,
	}
	transcoder, _ := egress.NewSupervisedSink("ffmpeg_transcoder", transcoderConfig)
	stream.AddSink(transcoder)

	if streamConfig.Pipeline.RecordSource {
		recorderConfig := map[string]interface{}{
			"stream":    stream,
			"sessionID": sessionID,
			"appConfig": m.AppConfig,
		}
		recorder, err := egress.NewSupervisedSink("flv_recorder", recorderConfig)
		if err != nil {
			slog.Error("Failed to create FLV recorder", "streamID", streamID, "error", err)
		} else {
			stream.AddSink(recorder)
		}
	}

	go stream.Run()
	stream.WaitUntilStopped()

}

func (m *Manager) addActiveStream(streamID string, stream *Stream) {
	m.streamsMutex.Lock()
	m.activeStreams[streamID] = stream
	atomic.AddInt64(&m.streamCount, 1)
	m.streamsMutex.Unlock()
}

func (m *Manager) removeActiveStream(streamID string) {
	slog.Info("START: Stream removed from active streams", "streamID", streamID)

	m.streamsMutex.Lock()
	if _, exists := m.activeStreams[streamID]; exists {
		delete(m.activeStreams, streamID)
		atomic.AddInt64(&m.streamCount, -1)
	}
	slog.Info("Stream removed from active streams", "streamID", streamID)
	m.streamsMutex.Unlock()
}

func (m *Manager) getAllSinkStatusesForStream(stream *Stream) ([]interface{}, error) {
	sinks := stream.GetAllSinks()

	if len(sinks) == 0 {
		return []interface{}{}, nil
	}

	statuses := make([]interface{}, 0, len(sinks))
	for _, sink := range sinks {
		if reporter, ok := sink.(StatusReporter); ok {
			statuses = append(statuses, reporter.GetStatus())
		} else {
			defaultStatus := map[string]string{"id": sink.ID()}
			statuses = append(statuses, defaultStatus)
		}
	}
	return statuses, nil
}

// handleEgressControlEvent reacts to redis pubsub   attach/detach sinks on the fly.
func (m *Manager) handleEgressControlEvent(event store.EgressControlEvent) {
	stream, ok := m.GetActiveStream(event.StreamID)
	if !ok {
		return
	}

	slog.Info("Received egress control command",
		"streamID", event.StreamID,
		"action", event.Action,
		"type", event.Config.Type,
		"egressID", event.EgressID)

	switch event.Action {
	case store.EgressActionStart:
		config := event.Config.Settings
		config["streamID"] = event.StreamID
		config["appConfig"] = m.AppConfig

		sink, err := egress.NewSupervisedSink(event.Config.Type, config)
		if err != nil {
			slog.Error("Failed to create dynamic egress sink",
				"streamID", event.StreamID,
				"egressID", event.EgressID,
				"error", err)
			return
		}
		stream.AddSink(sink)

	case store.EgressActionStop:
		stream.RemoveSink(event.EgressID)
	}
}
