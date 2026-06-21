package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MediaStreamInfo is one track we learned about from ffprobe or rtmp metadata.
type MediaStreamInfo struct {
	Index     int     `json:"index"`
	CodecType string  `json:"codec_type"`
	CodecName string  `json:"codec_name"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	FrameRate float64 `json:"r_frame_rate_float,omitempty"`
	Channels  int     `json:"channels,omitempty"`
}

// StreamProperties blocks pipeline setup until we know what we're dealing with.
type StreamProperties struct {
	mutex     sync.RWMutex
	ready     *sync.Cond
	isReady   bool
	waitAbort bool

	Streams []MediaStreamInfo
}

func NewStreamProperties() *StreamProperties {
	p := &StreamProperties{}
	// cond needs the same mutex we'll wait on
	p.ready = sync.NewCond(&p.mutex)
	return p
}

// SignalReady unblocks whoever's stuck in Wait().
func (p *StreamProperties) SignalReady() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.isReady {
		slog.Debug("SignalReady called but properties were already ready")
		return
	}

	slog.Debug("Signaling properties ready")
	p.isReady = true
	p.ready.Broadcast()
}

func (p *StreamProperties) Wait(timeout time.Duration) error {
	if timeout <= 0 {
		// caller wants to wait forever
		p.mutex.Lock()
		defer p.mutex.Unlock()

		for !p.isReady {
			p.ready.Wait()
		}
		return nil
	}

	p.mutex.Lock()
	p.waitAbort = false
	p.mutex.Unlock()

	// Timed wait without leaking a goroutine on timeout.
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	done := make(chan struct{})
	go func() {
		p.mutex.Lock()
		defer p.mutex.Unlock()
		for !p.isReady && !p.waitAbort {
			p.ready.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
		p.mutex.Lock()
		ready := p.isReady
		p.mutex.Unlock()
		if !ready {
			return fmt.Errorf("timed out after %v waiting for stream properties", timeout)
		}
		return nil
	case <-timer.C:
		p.mutex.Lock()
		p.waitAbort = true
		p.ready.Broadcast()
		p.mutex.Unlock()
		<-done
		return fmt.Errorf("timed out after %v waiting for stream properties", timeout)
	}
}

func (p *StreamProperties) WaitIndefinitely() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for !p.isReady {
		p.ready.Wait()
	}
}

func (p *StreamProperties) IsReady() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.isReady
}

func (p *StreamProperties) GetVideoStreams() []MediaStreamInfo {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	var videoStreams []MediaStreamInfo
	for _, s := range p.Streams {
		if s.CodecType == "video" {
			videoStreams = append(videoStreams, s)
		}
	}
	return videoStreams
}

func (p *StreamProperties) GetAudioStreams() []MediaStreamInfo {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	var audioStreams []MediaStreamInfo
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			audioStreams = append(audioStreams, s)
		}
	}
	return audioStreams
}

func (p *StreamProperties) NumAudioTracks() int {
	return len(p.GetAudioStreams())
}

// UpdateFromRtmpMetadata parses onMetaData   rtmp doesnt give us ffprobe.
func (p *StreamProperties) UpdateFromRtmpMetadata(meta map[string]interface{}) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// rtmp metadata parsing is messy; dont let a panic wedge the supervisor
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in UpdateFromRtmpMetadata", "error", r)
			// something is better than nothing for downstream sizing
			if len(p.Streams) == 0 {
				slog.Warn("Creating minimal stream info after panic")
				p.Streams = []MediaStreamInfo{
					{
						Index:     0,
						CodecType: "video",
						CodecName: "unknown",
					},
				}
			}
		}
	}()

	var videoStream MediaStreamInfo
	videoStream.CodecType = "video"
	videoStream.Index = 0

	var audioStream MediaStreamInfo
	audioStream.CodecType = "audio"
	audioStream.Index = 0

	hasVideo := false
	hasAudio := false

	// rtmp sends numbers as float64, always
	if val, ok := meta["width"]; ok {
		if width, ok := val.(float64); ok {
			videoStream.Width = int(width)
			slog.Debug("Parsed width", "width", videoStream.Width)
		} else {
			slog.Warn("Width is not float64", "type", fmt.Sprintf("%T", val), "value", val)
		}
	}

	if val, ok := meta["height"]; ok {
		if height, ok := val.(float64); ok {
			videoStream.Height = int(height)
			slog.Debug("Parsed height", "height", videoStream.Height)
		} else {
			slog.Warn("Height is not float64", "type", fmt.Sprintf("%T", val), "value", val)
		}
	}

	if val, ok := meta["framerate"]; ok {
		if framerate, ok := val.(float64); ok {
			videoStream.FrameRate = framerate
			slog.Debug("Parsed framerate", "framerate", videoStream.FrameRate)
		} else {
			slog.Warn("Framerate is not float64", "type", fmt.Sprintf("%T", val), "value", val)
		}
	}

	if val, ok := meta["videocodecid"]; ok {
		codecStr := fmt.Sprintf("%v", val)
		slog.Debug("Processing video codec", "raw_value", val, "string_value", codecStr)

		if codecStr == "7" {
			videoStream.CodecName = "h264"
		} else {
			videoStream.CodecName = codecStr
		}
		hasVideo = true
		slog.Debug("Video codec processed", "codec_name", videoStream.CodecName)
	}

	if val, ok := meta["audiocodecid"]; ok {
		codecStr := fmt.Sprintf("%v", val)
		slog.Debug("Processing audio codec", "raw_value", val, "string_value", codecStr)

		if codecStr == "10" {
			audioStream.CodecName = "aac"
		} else {
			audioStream.CodecName = codecStr
		}
		hasAudio = true
		slog.Debug("Audio codec processed", "codec_name", audioStream.CodecName)
	}

	if val, ok := meta["audiochannels"]; ok {
		if channels, ok := val.(float64); ok {
			audioStream.Channels = int(channels)
			slog.Debug("Parsed audio channels", "channels", audioStream.Channels)
		} else {
			slog.Warn("Audio channels is not float64", "type", fmt.Sprintf("%T", val), "value", val)
		}
	}

	p.Streams = []MediaStreamInfo{}
	streamIndex := 0

	if hasVideo {
		videoStream.Index = streamIndex
		p.Streams = append(p.Streams, videoStream)
		streamIndex++
		slog.Debug("Added video stream", "stream", videoStream)
	}

	if hasAudio {
		audioStream.Index = streamIndex
		p.Streams = append(p.Streams, audioStream)
		streamIndex++
		slog.Debug("Added audio stream", "stream", audioStream)
	}

	// empty props would stall the whole pipeline
	if len(p.Streams) == 0 {
		slog.Warn("No streams detected from metadata, creating default video stream")
		defaultStream := MediaStreamInfo{
			Index:     0,
			CodecType: "video",
			CodecName: "unknown",
		}
		p.Streams = append(p.Streams, defaultStream)
	}

	// inline count   NumAudioTracks would re-lock and deadlock
	audioTrackCount := 0
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			audioTrackCount++
		}
	}

	if !p.isReady {
		slog.Debug("Signaling properties ready after RTMP metadata update")
		p.isReady = true
		p.ready.Broadcast()
	}
}

func getMetadataKeys(meta map[string]interface{}) []string {
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	return keys
}

// UpdateFromFFprobeOutput is the srt path   ffprobe tells us everything.
func (p *StreamProperties) UpdateFromFFprobeOutput(ffprobeJSON []byte) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	slog.Debug("Starting ffprobe output processing", "json_size", len(ffprobeJSON))

	type ffprobeResult struct {
		Streams []struct {
			Index      int    `json:"index"`
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
	}

	var result ffprobeResult
	if err := json.Unmarshal(ffprobeJSON, &result); err != nil {
		slog.Error("Failed to unmarshal ffprobe JSON", "error", err, "json_preview", string(ffprobeJSON[:min(200, len(ffprobeJSON))]))
		return fmt.Errorf("failed to unmarshal ffprobe JSON: %w", err)
	}

	slog.Debug("Successfully parsed ffprobe JSON", "detected_streams", len(result.Streams))

	p.Streams = []MediaStreamInfo{}

	for i, s := range result.Streams {
		slog.Debug("Processing stream", "index", i, "codec_type", s.CodecType, "codec_name", s.CodecName)

		// ffprobe gives fps as a fraction string like "30/1"
		var frameRate float64
		if s.RFrameRate != "" {
			parts := strings.Split(s.RFrameRate, "/")
			if len(parts) == 2 {
				num, errNum := strconv.ParseFloat(parts[0], 64)
				den, errDen := strconv.ParseFloat(parts[1], 64)
				if errNum == nil && errDen == nil && den != 0 {
					frameRate = num / den
					slog.Debug("Calculated framerate", "stream_index", s.Index, "framerate", frameRate, "raw", s.RFrameRate)
				} else {
					slog.Warn("Failed to parse framerate", "stream_index", s.Index, "raw", s.RFrameRate, "num_err", errNum, "den_err", errDen)
				}
			} else {
				slog.Warn("Invalid framerate format", "stream_index", s.Index, "raw", s.RFrameRate)
			}
		}

		streamInfo := MediaStreamInfo{
			Index:     s.Index,
			CodecType: s.CodecType,
			CodecName: s.CodecName,
			Width:     s.Width,
			Height:    s.Height,
			FrameRate: frameRate,
			Channels:  s.Channels,
		}

		p.Streams = append(p.Streams, streamInfo)
		slog.Debug("Added stream", "stream_info", streamInfo)
	}

	// same deadlock reason as rtmp path
	audioTrackCount := 0
	videoTrackCount := 0
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			audioTrackCount++
		} else if s.CodecType == "video" {
			videoTrackCount++
		}
	}

	if !p.isReady {
		slog.Debug("Signaling properties ready after ffprobe update")
		p.isReady = true
		p.ready.Broadcast()
	} else {
		slog.Debug("Properties were already ready, updated existing data")
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
