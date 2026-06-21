package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muntader/zaynin-engine/internal/vod/service"

	"github.com/fsnotify/fsnotify"
	"github.com/muntader/zaynin-engine/internal/vod/types"
)

// LiveStreamWatcher uploads segments as they land on disk.
type LiveStreamWatcher struct {
	storageService *service.StorageService
	watcher        *fsnotify.Watcher
	watchDir       string
	httpEndpoint   string
	httpAuth       *HTTPAuthConfig
	httpClient     *http.Client
	outputs        []types.OutputStorage
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	fileQueue      chan FileEvent
	processedFiles map[string]time.Time
	mu             sync.RWMutex
	maxRetries     int
	retryDelay     time.Duration
	cleanupTicker  *time.Ticker
}

type FileEvent struct {
	Path      string
	EventType string
	Timestamp time.Time
}

type WatcherConfig struct {
	WatchDir        string
	HTTPEndpoint    string
	HTTPAuth        *HTTPAuthConfig
	Outputs         []types.OutputStorage
	MaxRetries      int
	RetryDelay      time.Duration
	WorkerCount     int
	CleanupInterval time.Duration
}

func NewLiveStreamWatcher(storageService *service.StorageService, config WatcherConfig) (*LiveStreamWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = time.Second * 2
	}
	if config.WorkerCount == 0 {
		config.WorkerCount = 5
	}
	if config.CleanupInterval == 0 {
		config.CleanupInterval = time.Minute * 10
	}

	lsw := &LiveStreamWatcher{
		storageService: storageService,
		watcher:        watcher,
		watchDir:       config.WatchDir,
		httpEndpoint:   config.HTTPEndpoint,
		httpAuth:       config.HTTPAuth,
		httpClient: &http.Client{
			Timeout: time.Second * 30,
		},
		outputs:        config.Outputs,
		ctx:            ctx,
		cancel:         cancel,
		fileQueue:      make(chan FileEvent, 100),
		processedFiles: make(map[string]time.Time),
		maxRetries:     config.MaxRetries,
		retryDelay:     config.RetryDelay,
		cleanupTicker:  time.NewTicker(config.CleanupInterval),
	}

	for i := 0; i < config.WorkerCount; i++ {
		lsw.wg.Add(1)
		go lsw.worker(i)
	}

	lsw.wg.Add(1)
	go lsw.cleanupWorker()

	return lsw, nil
}

func (lsw *LiveStreamWatcher) Start() error {

	err := lsw.watcher.Add(lsw.watchDir)
	if err != nil {
		return fmt.Errorf("failed to add directory to watcher: %w", err)
	}

	lsw.wg.Add(1)
	go lsw.eventListener()

	return nil
}

func (lsw *LiveStreamWatcher) Stop() {
	slog.Info("Stopping live stream watcher")

	lsw.cancel()
	lsw.watcher.Close()
	lsw.cleanupTicker.Stop()
	close(lsw.fileQueue)

	lsw.wg.Wait()
	slog.Info("Live stream watcher stopped")
}

func (lsw *LiveStreamWatcher) eventListener() {
	defer lsw.wg.Done()

	for {
		select {
		case <-lsw.ctx.Done():
			return
		case event, ok := <-lsw.watcher.Events:
			if !ok {
				return
			}

			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				if lsw.shouldProcessFile(event.Name) {
					fileEvent := FileEvent{
						Path:      event.Name,
						EventType: event.Op.String(),
						Timestamp: time.Now(),
					}

					select {
					case lsw.fileQueue <- fileEvent:
						slog.Debug("Queued file for processing", "file", event.Name, "event", event.Op.String())
					case <-lsw.ctx.Done():
						return
					default:
						slog.Warn("File queue full, dropping event", "file", event.Name)
					}
				}
			}
		case err, ok := <-lsw.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("Watcher error", "error", err)
		}
	}
}

func (lsw *LiveStreamWatcher) shouldProcessFile(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	validExts := []string{".m4s", ".ts", ".mpd", ".m3u8", ".mp4", ".webm"}

	isValid := false
	for _, validExt := range validExts {
		if ext == validExt {
			isValid = true
			break
		}
	}

	if !isValid {
		return false
	}

	// fsnotify fires twice on fast writes   debounce a bit
	lsw.mu.RLock()
	lastProcessed, exists := lsw.processedFiles[filePath]
	lsw.mu.RUnlock()

	if exists && time.Since(lastProcessed) < time.Second*5 {
		return false
	}

	return true
}

func (lsw *LiveStreamWatcher) worker(workerID int) {
	defer lsw.wg.Done()

	slog.Info("Worker started", "worker_id", workerID)

	for {
		select {
		case <-lsw.ctx.Done():
			slog.Info("Worker stopping", "worker_id", workerID)
			return
		case fileEvent, ok := <-lsw.fileQueue:
			if !ok {
				slog.Info("Worker queue closed", "worker_id", workerID)
				return
			}

			lsw.processFile(fileEvent, workerID)
		}
	}
}

func (lsw *LiveStreamWatcher) processFile(fileEvent FileEvent, workerID int) {
	// packager might still be flushing the file
	time.Sleep(time.Millisecond * 100)
	if !lsw.isFileReady(fileEvent.Path) {
		slog.Warn("File not ready, skipping", "file", fileEvent.Path, "worker", workerID)
		return
	}

	slog.Info("Processing file", "file", fileEvent.Path, "worker", workerID)

	lsw.mu.Lock()
	lsw.processedFiles[fileEvent.Path] = time.Now()
	lsw.mu.Unlock()

	var wg sync.WaitGroup

	if lsw.httpEndpoint != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lsw.uploadToHTTP(fileEvent.Path, workerID)
		}()
	}

	for _, output := range lsw.outputs {
		wg.Add(1)
		go func(out types.OutputStorage) {
			defer wg.Done()
			lsw.uploadToStorage(fileEvent.Path, out, workerID)
		}(output)
	}

	wg.Wait()
}

func (lsw *LiveStreamWatcher) isFileReady(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	file.Close()

	stat1, err := os.Stat(filePath)
	if err != nil {
		return false
	}

	time.Sleep(time.Millisecond * 50)

	stat2, err := os.Stat(filePath)
	if err != nil {
		return false
	}

	return stat1.Size() == stat2.Size() && stat1.Size() > 0
}

func (lsw *LiveStreamWatcher) uploadToHTTP(filePath string, workerID int) {
	fileName := filepath.Base(filePath)

	var url string
	if lsw.httpAuth != nil && lsw.httpAuth.StreamID != "" && lsw.httpAuth.SessionID != "" {
		baseURL := strings.TrimSuffix(lsw.httpEndpoint, "/")
		url = fmt.Sprintf("%s/streams/%s/%s/%s",
			baseURL, lsw.httpAuth.StreamID, lsw.httpAuth.SessionID, fileName)
	} else {
		url = strings.TrimSuffix(lsw.httpEndpoint, "/") + "/" + fileName
	}

	for attempt := 1; attempt <= lsw.maxRetries; attempt++ {
		err := lsw.httpUploadAttempt(filePath, url)
		if err == nil {
			slog.Info("HTTP upload successful", "file", fileName, "url", url, "worker", workerID)
			return
		}

		slog.Warn("HTTP upload failed", "file", fileName, "url", url, "attempt", attempt, "error", err, "worker", workerID)

		if attempt < lsw.maxRetries {
			time.Sleep(lsw.retryDelay)
		}
	}

	slog.Error("HTTP upload failed after all retries", "file", fileName, "url", url, "worker", workerID)
}

func (lsw *LiveStreamWatcher) httpUploadAttempt(filePath, url string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	req, err := http.NewRequestWithContext(lsw.ctx, "PUT", url, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if lsw.httpAuth != nil && lsw.httpAuth.Token != "" {
		req.Header.Set("Authorization", "Bearer "+lsw.httpAuth.Token)
	}

	if lsw.httpAuth != nil && lsw.httpAuth.Headers != nil {
		for key, value := range lsw.httpAuth.Headers {
			req.Header.Set(key, value)
		}
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".m4s", ".mp4":
		req.Header.Set("Content-Type", "video/mp4")
	case ".ts":
		req.Header.Set("Content-Type", "video/mp2t")
	case ".mpd":
		req.Header.Set("Content-Type", "application/dash+xml")
	case ".m3u8":
		req.Header.Set("Content-Type", "application/vnd.apple.mpegurl")
	default:
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(content)))

	resp, err := lsw.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP upload failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (lsw *LiveStreamWatcher) uploadToStorage(filePath string, output types.OutputStorage, workerID int) {
	// StorageService wants a directory   stage a single-file temp dir
	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("live-stream-upload-%d-%d", workerID, time.Now().UnixNano()))

	if err := os.MkdirAll(tempDir, 0755); err != nil {
		slog.Error("Failed to create temp directory", "error", err, "worker", workerID)
		return
	}
	defer os.RemoveAll(tempDir)

	fileName := filepath.Base(filePath)
	tempFilePath := filepath.Join(tempDir, fileName)

	if err := lsw.copyFile(filePath, tempFilePath); err != nil {
		slog.Error("Failed to copy file to temp directory", "error", err, "file", filePath, "worker", workerID)
		return
	}

	for attempt := 1; attempt <= lsw.maxRetries; attempt++ {
		err := lsw.storageService.UploadDirectory(lsw.ctx, output, tempDir)
		if err == nil {
			slog.Info("Storage upload successful", "file", fileName, "provider", output.Provider, "worker", workerID)
			return
		}

		slog.Warn("Storage upload failed", "file", fileName, "provider", output.Provider, "attempt", attempt, "error", err, "worker", workerID)

		if attempt < lsw.maxRetries {
			time.Sleep(lsw.retryDelay)
		}
	}

	slog.Error("Storage upload failed after all retries", "file", fileName, "provider", output.Provider, "worker", workerID)
}

func (lsw *LiveStreamWatcher) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

func (lsw *LiveStreamWatcher) cleanupWorker() {
	defer lsw.wg.Done()

	for {
		select {
		case <-lsw.ctx.Done():
			return
		case <-lsw.cleanupTicker.C:
			lsw.cleanup()
		}
	}
}

func (lsw *LiveStreamWatcher) cleanup() {
	lsw.mu.Lock()
	defer lsw.mu.Unlock()

	cutoff := time.Now().Add(-time.Hour)
	for filePath, timestamp := range lsw.processedFiles {
		if timestamp.Before(cutoff) {
			delete(lsw.processedFiles, filePath)
		}
	}

	slog.Debug("Cleanup completed", "remaining_entries", len(lsw.processedFiles))
}

func (lsw *LiveStreamWatcher) GetStats() map[string]interface{} {
	lsw.mu.RLock()
	defer lsw.mu.RUnlock()

	return map[string]interface{}{
		"processed_files_count": len(lsw.processedFiles),
		"queue_length":          len(lsw.fileQueue),
		"watch_directory":       lsw.watchDir,
		"http_endpoint":         lsw.httpEndpoint,
		"output_count":          len(lsw.outputs),
	}
}
