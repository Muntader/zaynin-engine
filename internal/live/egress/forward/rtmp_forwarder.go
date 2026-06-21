package forward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"github.com/yutopp/go-rtmp"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/media"
	flvtag "github.com/yutopp/go-flv/tag"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

func init() {
	egress.RegisterSink("rtmp_push", NewRTMPForwarder)
}

const (
	defaultChunkSize    = 4096
	defaultPacketBuffer = 1024
	defaultShutdownSecs = 10
	chunkStreamAudio    = 4
	chunkStreamVideo    = 6
	chunkStreamData     = 8
	defaultPort         = "1935"
)

type ConnectionState int32

const (
	StateDisconnected ConnectionState = iota
	StateConnecting
	StateConnected
	StatePublishing
	StateReconnecting
	StateShutdown
)

func (s ConnectionState) String() string {
	states := [...]string{"Disconnected", "Connecting", "Connected", "Publishing", "Reconnecting", "Shutdown"}
	if s < 0 || int(s) >= len(states) {
		return "Unknown"
	}
	return states[s]
}

// rtmpForwarderConfig decodes the loose map[string]interface{} from redis.
type rtmpForwarderConfig struct {
	ID                 string `mapstructure:"id"`
	RemoteURL          string `mapstructure:"remoteURL"`
	APIKey             string `mapstructure:"apiKey"`
	Platform           string `mapstructure:"platform"`
	PacketBuffer       int    `mapstructure:"packet_buffer"`
	ShutdownTimeoutSec int    `mapstructure:"shutdown_timeout_secs"`
}

// RTMPPusherConfig is the parsed, ready-to-dial rtmp target.
type RTMPPusherConfig struct {
	ID       string
	Protocol string
	Addr     string
	App      string
	APIKey   string
	TCURL    string
	Platform string
}

// RTMPForwarder pushes to youtube/twitch/etc with reconnect + header replay.
type RTMPForwarder struct {
	config          RTMPPusherConfig
	client          *rtmp.ClientConn
	stream          *rtmp.Stream
	connMu          sync.RWMutex
	disconnect      chan struct{} // recreated per connection attempt
	shutdownTimeout time.Duration

	state  int32
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	packets    chan *flvtag.FlvTag
	bufferPool *sync.Pool

	stats struct {
		mu              sync.RWMutex
		packetsReceived uint64
		packetsSent     uint64
		packetsDropped  uint64
		reconnectCount  uint64
		lastError       error
		lastErrorTime   time.Time
	}

	// reconnects are useless without resending aac/avc config headers
	cacheMu           sync.RWMutex
	cachedMetadata    *flvtag.FlvTag
	cachedAudioConfig *flvtag.FlvTag
	cachedVideoConfig *flvtag.FlvTag
	hasSentHeaders    atomic.Bool
}

func NewRTMPForwarder(config map[string]interface{}) (egress.Sink, error) {
	cfg := rtmpForwarderConfig{
		PacketBuffer:       defaultPacketBuffer,
		ShutdownTimeoutSec: defaultShutdownSecs,
	}
	if err := mapstructure.Decode(config, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode rtmp_push config: %w", err)
	}

	if cfg.ID == "" || cfg.RemoteURL == "" || cfg.APIKey == "" {
		return nil, fmt.Errorf("rtmp_push config missing required fields: id, remoteURL, or apiKey")
	}

	parsedCfg, err := parseAndBuildConfig(cfg.ID, cfg.Platform, cfg.RemoteURL, cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rtmp push config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	forwarder := &RTMPForwarder{
		config:          *parsedCfg,
		packets:         make(chan *flvtag.FlvTag, cfg.PacketBuffer),
		shutdownTimeout: time.Duration(cfg.ShutdownTimeoutSec) * time.Second,
		ctx:             ctx,
		cancel:          cancel,
		bufferPool: &sync.Pool{
			New: func() interface{} { return new(bytes.Buffer) },
		},
	}
	forwarder.hasSentHeaders.Store(false)
	atomic.StoreInt32(&forwarder.state, int32(StateDisconnected))

	forwarder.wg.Add(1)
	go forwarder.connectionManager()

	return forwarder, nil
}

func (f *RTMPForwarder) ID() string {
	return f.config.ID
}

func (f *RTMPForwarder) Close() error {
	f.setState(StateShutdown)
	f.cancel()

	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		//slog.Info("Forwarder sink shutdown complete.", "id", f.ID())
	case <-time.After(f.shutdownTimeout):
		slog.Warn("Forwarder sink shutdown timeout reached.", "id", f.ID())
	}
	return nil
}

func (f *RTMPForwarder) WritePacket(packet *media.Packet) error {
	if f.ctx.Err() != nil {
		return f.ctx.Err()
	}

	var flvTag flvtag.FlvTag
	timestamp := uint32(packet.Timestamp / time.Millisecond)

	switch packet.Type {
	case media.PacketTypeMetadata:
		var script flvtag.ScriptData
		if err := flvtag.DecodeScriptData(bytes.NewReader(packet.Data), &script); err != nil {
			slog.Warn("Failed to decode metadata packet, dropping", "id", f.ID(), "error", err)
			return nil
		}
		flvTag.TagType = flvtag.TagTypeScriptData
		flvTag.Data = &script
	case media.PacketTypeAudio:
		var audio flvtag.AudioData
		if err := flvtag.DecodeAudioData(bytes.NewReader(packet.Data), &audio); err != nil {
			slog.Warn("Failed to decode audio packet, dropping", "id", f.ID(), "error", err)
			return nil
		}
		flvTag.TagType = flvtag.TagTypeAudio
		flvTag.Timestamp = timestamp
		flvTag.Data = &audio
	case media.PacketTypeVideo:
		var video flvtag.VideoData
		if err := flvtag.DecodeVideoData(bytes.NewReader(packet.Data), &video); err != nil {
			slog.Warn("Failed to decode video packet, dropping", "id", f.ID(), "error", err)
			return nil
		}
		flvTag.TagType = flvtag.TagTypeVideo
		flvTag.Timestamp = timestamp
		flvTag.Data = &video
	default:
		return nil // Ignore unknown packet types
	}

	tagCopy, err := cloneFlvTag(flvTag)
	if err != nil {
		slog.Warn("Failed to clone FLV tag, dropping", "id", f.ID(), "error", err)
		return nil
	}

	f.cachePacketIfHeader(tagCopy)

	f.stats.mu.Lock()
	f.stats.packetsReceived++
	f.stats.mu.Unlock()

	select {
	case f.packets <- tagCopy:
	default:
		f.stats.mu.Lock()
		f.stats.packetsDropped++
		f.stats.mu.Unlock()
	}

	return nil
}

func (f *RTMPForwarder) GetStatus() interface{} {
	f.stats.mu.RLock()
	defer f.stats.mu.RUnlock()

	var errString string
	if f.stats.lastError != nil {
		errString = f.stats.lastError.Error()
	}

	return struct {
		ID              string `json:"id"`
		Platform        string `json:"platform"`
		State           string `json:"state"`
		PacketsReceived uint64 `json:"packets_received"`
		PacketsSent     uint64 `json:"packets_sent"`
		PacketsDropped  uint64 `json:"packets_dropped"`
		ReconnectCount  uint64 `json:"reconnect_count"`
		LastError       string `json:"last_error,omitempty"`
		LastErrorTime   string `json:"last_error_time,omitempty"`
	}{
		ID:              f.ID(),
		Platform:        f.config.Platform,
		State:           ConnectionState(atomic.LoadInt32(&f.state)).String(),
		PacketsReceived: f.stats.packetsReceived,
		PacketsSent:     f.stats.packetsSent,
		PacketsDropped:  f.stats.packetsDropped,
		ReconnectCount:  f.stats.reconnectCount,
		LastError:       errString,
		LastErrorTime:   f.stats.lastErrorTime.Format(time.RFC3339),
	}
}

func parseAndBuildConfig(id, platform, ingestURL, apiKey string) (*RTMPPusherConfig, error) {
	parsedURL, err := url.Parse(ingestURL)
	if err != nil {
		return nil, fmt.Errorf("invalid ingest URL: %w", err)
	}
	if parsedURL.Scheme != "rtmp" && parsedURL.Scheme != "rtmps" {
		return nil, fmt.Errorf("unsupported scheme: %s (expected rtmp or rtmps)", parsedURL.Scheme)
	}
	port := parsedURL.Port()
	if port == "" {
		port = defaultPort
	}
	app := strings.Trim(parsedURL.Path, "/")
	if app == "" {
		slog.Warn("Ingest URL has an empty path component, using empty application name.", "id", id, "url", ingestURL)
	}
	tcURL := *parsedURL
	tcURL.Path = "/" + app
	if tcURL.Port() == defaultPort && parsedURL.Scheme == "rtmp" {
		tcURL.Host = tcURL.Hostname()
	}

	return &RTMPPusherConfig{
		ID:       id,
		Platform: platform,
		Protocol: parsedURL.Scheme,
		Addr:     fmt.Sprintf("%s:%s", parsedURL.Hostname(), port),
		App:      app,
		APIKey:   apiKey,
		TCURL:    tcURL.String(),
	}, nil
}

func (f *RTMPForwarder) setState(state ConnectionState) {
	atomic.StoreInt32(&f.state, int32(state))
}

func (f *RTMPForwarder) recordError(err error) {
	if err == nil {
		return
	}
	f.stats.mu.Lock()
	defer f.stats.mu.Unlock()
	f.stats.lastError = err
	f.stats.lastErrorTime = time.Now()
}

func (f *RTMPForwarder) connectionManager() {
	defer f.wg.Done()
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for f.ctx.Err() == nil {
		f.setState(StateConnecting)
		stream, err := f.connectAndPublish()
		if err != nil {
			f.recordError(err)
			f.setState(StateReconnecting)
			f.stats.mu.Lock()
			f.stats.reconnectCount++
			f.stats.mu.Unlock()
			slog.Error("Connection failed. Retrying...", "id", f.ID(), "error", err, "backoff", backoff)

			select {
			case <-time.After(backoff):
				if backoff < maxBackoff {
					backoff *= 2
				}
				continue
			case <-f.ctx.Done():
				return
			}
		}

		backoff = time.Second
		f.setState(StatePublishing)

		f.wg.Add(1)
		go f.packetProcessor(stream)

		select {
		case <-f.disconnect:
			slog.Warn("Stream connection lost. Preparing to reconnect.", "id", f.ID())
		case <-f.ctx.Done():
		}
		f.cleanupConnection()
	}
}

func (f *RTMPForwarder) connectAndPublish() (*rtmp.Stream, error) {
	client, err := rtmp.Dial(f.config.Protocol, f.config.Addr, &rtmp.ConnConfig{})
	if err != nil {
		return nil, fmt.Errorf("dial failed: %w", err)
	}

	f.disconnect = make(chan struct{})
	var stream *rtmp.Stream

	err = func() error {
		if e := client.Connect(&rtmpmsg.NetConnectionConnect{Command: rtmpmsg.NetConnectionConnectCommand{
			App:      f.config.App,
			FlashVer: "ZayninEngine/1.0",
			TCURL:    f.config.TCURL,
		}}); e != nil {
			return fmt.Errorf("connect command failed: %w", e)
		}
		var e error
		stream, e = client.CreateStream(&rtmpmsg.NetConnectionCreateStream{}, uint32(defaultChunkSize))
		if e != nil {
			return fmt.Errorf("create stream failed: %w", e)
		}
		if e = stream.Publish(&rtmpmsg.NetStreamPublish{
			PublishingName: f.config.APIKey,
			PublishingType: "live",
		}); e != nil {
			return fmt.Errorf("publish command failed: %w", e)
		}
		return nil
	}()

	if err != nil {
		_ = client.Close()
		return nil, err
	}

	f.connMu.Lock()
	f.client = client
	f.stream = stream
	f.connMu.Unlock()

	f.setState(StateConnected)
	return stream, nil
}

func (f *RTMPForwarder) packetProcessor(stream *rtmp.Stream) {
	defer f.wg.Done()

	if f.hasSentHeaders.Load() {
		f.cacheMu.RLock()
		if err := f.sendHeader(stream, f.cachedMetadata, "metadata"); err != nil {
			f.cacheMu.RUnlock()
			return
		}
		if err := f.sendHeader(stream, f.cachedAudioConfig, "audio config"); err != nil {
			f.cacheMu.RUnlock()
			return
		}
		if err := f.sendHeader(stream, f.cachedVideoConfig, "video config"); err != nil {
			f.cacheMu.RUnlock()
			return
		}
		f.cacheMu.RUnlock()
	}

	for {
		select {
		case <-f.ctx.Done():
			return
		case tag, ok := <-f.packets:
			if !ok {
				return
			}
			if ConnectionState(atomic.LoadInt32(&f.state)) != StatePublishing {
				f.stats.mu.Lock()
				f.stats.packetsDropped++
				f.stats.mu.Unlock()
				continue
			}
			if err := f.sendPacket(stream, tag); err != nil {
				slog.Error("Send failed, triggering disconnect", "id", f.ID(), "error", err)
				f.recordError(err)
				close(f.disconnect)
				return
			}
			f.stats.mu.Lock()
			f.stats.packetsSent++
			f.stats.mu.Unlock()

			if !f.hasSentHeaders.Load() {
				f.hasSentHeaders.Store(true)
			}
		}
	}
}

func (f *RTMPForwarder) sendHeader(stream *rtmp.Stream, tag *flvtag.FlvTag, name string) error {
	if tag == nil {
		return nil
	}
	if err := f.sendPacket(stream, tag); err != nil {
		slog.Error("Failed to send cached data, triggering disconnect", "id", f.ID(), "name", name, "error", err)
		f.recordError(err)
		close(f.disconnect)
		return err
	}
	return nil
}

func (f *RTMPForwarder) cleanupConnection() {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.client != nil {
		_ = f.client.Close()
		f.client = nil
		f.stream = nil
	}
}

func (f *RTMPForwarder) cachePacketIfHeader(tag *flvtag.FlvTag) {
	if f.hasSentHeaders.Load() {
		return
	}

	isHeader := false
	switch data := tag.Data.(type) {
	case *flvtag.ScriptData:
		isHeader = true
		f.cacheMu.Lock()
		f.cachedMetadata = tag
		f.cacheMu.Unlock()
	case *flvtag.AudioData:
		if data.SoundFormat == flvtag.SoundFormatAAC && data.AACPacketType == flvtag.AACPacketTypeSequenceHeader {
			isHeader = true
			f.cacheMu.Lock()
			f.cachedAudioConfig = tag
			f.cacheMu.Unlock()
		}
	case *flvtag.VideoData:
		if data.FrameType == flvtag.FrameTypeKeyFrame && data.AVCPacketType == flvtag.AVCPacketTypeSequenceHeader {
			isHeader = true
			f.cacheMu.Lock()
			f.cachedVideoConfig = tag
			f.cacheMu.Unlock()
		}
	}
	if isHeader {
		slog.Debug("Cached header packet", "id", f.ID(), "type", tag.TagType)
	}
}

func (f *RTMPForwarder) sendPacket(stream *rtmp.Stream, tag *flvtag.FlvTag) error {
	switch data := tag.Data.(type) {
	case *flvtag.AudioData:
		return f.sendAudio(stream, data, tag.Timestamp)
	case *flvtag.VideoData:
		return f.sendVideo(stream, data, tag.Timestamp)
	case *flvtag.ScriptData:
		return f.sendMetadata(stream, data, tag.Timestamp)
	default:
		return nil
	}
}

func (f *RTMPForwarder) sendAudio(stream *rtmp.Stream, data *flvtag.AudioData, timestamp uint32) error {
	buf := f.getBuffer()
	defer f.putBuffer(buf)
	if err := flvtag.EncodeAudioData(buf, data); err != nil {
		return err
	}
	return stream.Write(chunkStreamAudio, timestamp, &rtmpmsg.AudioMessage{Payload: buf})
}

func (f *RTMPForwarder) sendVideo(stream *rtmp.Stream, data *flvtag.VideoData, timestamp uint32) error {
	buf := f.getBuffer()
	defer f.putBuffer(buf)
	if err := flvtag.EncodeVideoData(buf, data); err != nil {
		return err
	}
	return stream.Write(chunkStreamVideo, timestamp, &rtmpmsg.VideoMessage{Payload: buf})
}

func (f *RTMPForwarder) sendMetadata(stream *rtmp.Stream, data *flvtag.ScriptData, timestamp uint32) error {
	buf := f.getBuffer()
	defer f.putBuffer(buf)
	if err := flvtag.EncodeScriptData(buf, data); err != nil {
		return err
	}
	msg := &rtmpmsg.DataMessage{Name: "onMetaData", Encoding: rtmpmsg.EncodingTypeAMF0, Body: buf}
	return stream.Write(chunkStreamData, timestamp, msg)
}

func (f *RTMPForwarder) getBuffer() *bytes.Buffer {
	buf := f.bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func (f *RTMPForwarder) putBuffer(buf *bytes.Buffer) {
	f.bufferPool.Put(buf)
}

// cloneFlvTag heap-allocates tag data so async consumers never hold stack pointers.
func cloneFlvTag(src flvtag.FlvTag) (*flvtag.FlvTag, error) {
	dst := &flvtag.FlvTag{
		TagType:   src.TagType,
		Timestamp: src.Timestamp,
		StreamID:  src.StreamID,
	}

	switch data := src.Data.(type) {
	case *flvtag.ScriptData:
		buf := &bytes.Buffer{}
		if err := flvtag.EncodeScriptData(buf, data); err != nil {
			return nil, fmt.Errorf("encode script data: %w", err)
		}
		script := &flvtag.ScriptData{}
		if err := flvtag.DecodeScriptData(bytes.NewReader(buf.Bytes()), script); err != nil {
			return nil, fmt.Errorf("decode script data: %w", err)
		}
		dst.Data = script
	case *flvtag.AudioData:
		payload, err := io.ReadAll(data)
		if err != nil {
			return nil, fmt.Errorf("read audio payload: %w", err)
		}
		dst.Data = &flvtag.AudioData{
			SoundFormat:   data.SoundFormat,
			SoundRate:     data.SoundRate,
			SoundSize:     data.SoundSize,
			SoundType:     data.SoundType,
			AACPacketType: data.AACPacketType,
			Data:          bytes.NewReader(payload),
		}
	case *flvtag.VideoData:
		payload, err := io.ReadAll(data)
		if err != nil {
			return nil, fmt.Errorf("read video payload: %w", err)
		}
		dst.Data = &flvtag.VideoData{
			FrameType:       data.FrameType,
			CodecID:         data.CodecID,
			AVCPacketType:   data.AVCPacketType,
			CompositionTime: data.CompositionTime,
			Data:            bytes.NewReader(payload),
		}
	default:
		return nil, fmt.Errorf("unsupported flv tag data type %T", src.Data)
	}

	return dst, nil
}
