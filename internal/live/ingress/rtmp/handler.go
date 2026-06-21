package rtmp

import (
	"bytes"
	"errors"
	"log/slog"

	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/media"

	"io"
	"sync"
	"time"

	"github.com/yutopp/go-flv/tag"
	"github.com/yutopp/go-rtmp"
	"github.com/yutopp/go-rtmp/message"
)

// Source adapts one rtmp connection into ingress.Source packets.
type Source struct {
	id         string
	packetChan chan *media.Packet
	closeOnce  sync.Once
	stopChan   chan struct{}
}

func (s *Source) ID() string { return s.id }
func (s *Source) ReadPacket() (*media.Packet, error) {
	select {
	case p, ok := <-s.packetChan:
		if !ok {
			return nil, io.EOF
		}
		return p, nil
	case <-s.stopChan:
		return nil, io.EOF
	}
}
func (s *Source) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopChan)
		// drain so handler goroutines dont block on send
		for range s.packetChan {
		}
	})
	return nil
}

// Handler bridges go-rtmp callbacks into our stream manager.
type Handler struct {
	rtmp.DefaultHandler
	manager   *core.Manager
	stream    *core.Stream
	source    *Source
	appConfig *configTypes.Config
}

// OnPublish checks redis then spins up the stream if the key is valid.
func (h *Handler) OnPublish(ctx *rtmp.StreamContext, timestamp uint32, cmd *message.NetStreamPublish) error {
	rtmpKey := cmd.PublishingName
	if rtmpKey == "" {
		slog.Error("RTMP connection rejected: Publishing name (stream key) is empty.")
		return errors.New("stream key cannot be empty")
	}

	h.source = &Source{
		id:         rtmpKey,
		packetChan: make(chan *media.Packet, 2048), // obs can burst pretty hard
		stopChan:   make(chan struct{}),
	}

	stream, err := h.manager.StartStream(h.source)
	if err != nil {
		slog.Error("RTMP connection rejected by manager", "key", rtmpKey, "error", err)
		return err
	}

	h.stream = stream

	slog.Info("Stream is now active. Ready to receive media.", "key", rtmpKey, "stream_id", h.stream.ID())

	return nil
}

// OnClose   manager owns lifecycle, not the handler.
func (h *Handler) OnClose() {
	if h.stream != nil {
		slog.Info("RTMP connection closed.", "stream_id", h.stream.ID())
		h.manager.DeactivateStream(h.stream.ID())
	}
}

func (h *Handler) OnSetDataFrame(timestamp uint32, data *message.NetStreamSetDataFrame) error {
	slog.Info("Received data frame")

	r := bytes.NewReader(data.Payload)
	var script tag.ScriptData
	if err := tag.DecodeScriptData(r, &script); err != nil {
		slog.Error("Failed to decode script data", "error", err)
		// supervisor cant wait forever on bad metadata
		if h.stream != nil {
			h.stream.Properties().SignalReady()
		}
		return nil
	}

	metaData, exists := script.Objects["onMetaData"]
	if !exists {
		slog.Warn("No onMetaData object found in script data")
		if h.stream != nil {
			h.stream.Properties().SignalReady()
		}
		return nil
	}

	metaMap := map[string]interface{}(metaData)
	if len(metaMap) == 0 {
		slog.Info("Inspector found empty onMetaData object.")
		if h.stream != nil {
			h.stream.Properties().SignalReady()
		}
		return nil
	}

	if h.stream != nil {
		h.stream.Properties().UpdateFromRtmpMetadata(metaMap)

		packet := &media.Packet{
			Type:             media.PacketTypeMetadata,
			Timestamp:        0,
			Data:             data.Payload,
			IsSequenceHeader: true,
		}

		h.source.packetChan <- packet
		h.stream.Properties().SignalReady()
	}

	return nil
}

func (h *Handler) OnAudio(timestamp uint32, payload io.Reader) error {
	if h.source == nil {
		return nil
	}
	data, err := io.ReadAll(payload)
	if len(data) == 0 || err != nil {
		return err
	}

	var audioData tag.AudioData
	err = tag.DecodeAudioData(bytes.NewReader(data), &audioData)
	if err != nil {
		return err
	}

	// aac needs the sequence header cached for late sinks
	isHeader := audioData.SoundFormat == tag.SoundFormatAAC &&
		audioData.AACPacketType == tag.AACPacketTypeSequenceHeader

	packet := &media.Packet{
		Type:             media.PacketTypeAudio,
		Timestamp:        time.Duration(timestamp) * time.Millisecond,
		Data:             data,
		IsSequenceHeader: isHeader,
	}
	h.source.packetChan <- packet
	return nil
}

func (h *Handler) OnVideo(timestamp uint32, payload io.Reader) error {
	if h.source == nil {
		return nil
	}
	data, _ := io.ReadAll(payload)
	if len(data) == 0 {
		return nil
	}
	var videoData tag.VideoData
	_ = tag.DecodeVideoData(bytes.NewReader(data), &videoData)

	//isHeader := videoData.CodecID == tag.CodecIDAVC &&
	//	videoData.AVCPacketType == tag.AVCPacketTypeSequenceHeader

	packet := &media.Packet{
		Type:             media.PacketTypeVideo,
		Timestamp:        time.Duration(timestamp) * time.Millisecond,
		Data:             data,
		IsKeyframe:       videoData.FrameType == tag.FrameTypeKeyFrame,
		IsSequenceHeader: true,
	}
	h.source.packetChan <- packet
	return nil
}

func (s *Source) Format() string {
	return "flv"
}
