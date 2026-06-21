package recorder

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/media"

	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"
)

func init() {
	egress.RegisterSink("flv_recorder", NewFlvRecorder)
}

type FlvRecorderSink struct {
	id              string
	file            *os.File
	flvEnc          *flv.Encoder
	packetChan      chan *media.Packet
	stopChan        chan struct{}
	wg              sync.WaitGroup
	closeOnce       sync.Once
	liveFilePath    string
	archiveFilePath string
	cleanupAction   media.CleanupAction
}

func NewFlvRecorder(config map[string]interface{}) (egress.Sink, error) {
	stream, ok := config["stream"].(*core.Stream)
	if !ok {
		return nil, errors.New("ffmpeg_transcoder requires a valid 'stream' object in config")
	}

	sessionID, _ := config["sessionID"].(string)
	appConfig, _ := config["appConfig"].(configTypes.Config)

	liveDir := filepath.Join(appConfig.Storage.Paths.LiveMedia, stream.ID(), sessionID, "recording")
	if err := os.MkdirAll(liveDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create recording live directory: %w", err)
	}
	fileName := fmt.Sprintf("%s-%d.flv", stream.ID(), time.Now().Unix())
	liveFilePath := filepath.Join(liveDir, fileName)

	archiveDir := filepath.Join(appConfig.Storage.Paths.LiveArchive, stream.ID(), sessionID, "recording")
	archiveFilePath := filepath.Join(archiveDir, fileName)

	f, err := os.Create(liveFilePath)
	if err != nil {
		return nil, err
	}
	enc, _ := flv.NewEncoder(f, flv.FlagsAudio|flv.FlagsVideo)

	rec := &FlvRecorderSink{
		id:              fmt.Sprintf("recorder-flv-%s", stream.ID()),
		file:            f,
		flvEnc:          enc,
		packetChan:      make(chan *media.Packet, 256),
		stopChan:        make(chan struct{}),
		wg:              sync.WaitGroup{},
		closeOnce:       sync.Once{},
		liveFilePath:    liveFilePath,
		archiveFilePath: archiveFilePath,
	}

	rec.wg.Add(1)
	go rec.run()

	return rec, nil
}

func (r *FlvRecorderSink) ID() string { return r.id }

// Close always archives   thats the point of this sink.
func (r *FlvRecorderSink) Close() error {
	r.closeOnce.Do(func() {
		close(r.stopChan)
		r.wg.Wait()

		finalDir := filepath.Dir(r.archiveFilePath)
		if err := os.MkdirAll(finalDir, 0755); err != nil {
			slog.Error("Failed to create final recording directory", "id", r.id, "directory", finalDir, "error", err)
			return
		}
		if err := os.Rename(r.liveFilePath, r.archiveFilePath); err != nil {
			slog.Error("Failed to archive recording", "id", r.id, "from", r.liveFilePath, "to", r.archiveFilePath, "error", err)
		}
	})
	return nil
}

func (r *FlvRecorderSink) WritePacket(packet *media.Packet) error {
	select {
	case r.packetChan <- packet:
	default:
		slog.Warn("FlvRecorder channel full, dropping packet", "stream_id", r.id)
	}
	return nil
}

func (r *FlvRecorderSink) run() {
	defer r.wg.Done()
	defer r.file.Close()

	for {
		select {
		case packet, ok := <-r.packetChan:
			if !ok {
				return
			}
			r.encodeAndWrite(packet)
		case <-r.stopChan:
			for len(r.packetChan) > 0 {
				r.encodeAndWrite(<-r.packetChan)
			}
			return
		}
	}
}

func (r *FlvRecorderSink) encodeAndWrite(packet *media.Packet) {
	var flvTag flvtag.FlvTag
	timestamp := uint32(packet.Timestamp / time.Millisecond)

	switch packet.Type {
	case media.PacketTypeMetadata:
		var script flvtag.ScriptData
		_ = flvtag.DecodeScriptData(bytes.NewReader(packet.Data), &script)
		flvTag.TagType = flvtag.TagTypeScriptData
		flvTag.Data = &script
	case media.PacketTypeAudio:
		var audio flvtag.AudioData
		_ = flvtag.DecodeAudioData(bytes.NewReader(packet.Data), &audio)
		flvTag.TagType = flvtag.TagTypeAudio
		flvTag.Timestamp = timestamp
		flvTag.Data = &audio
	case media.PacketTypeVideo:
		var video flvtag.VideoData
		_ = flvtag.DecodeVideoData(bytes.NewReader(packet.Data), &video)
		flvTag.TagType = flvtag.TagTypeVideo
		flvTag.Timestamp = timestamp
		flvTag.Data = &video
	}

	if err := r.flvEnc.Encode(&flvTag); err != nil {
		slog.Error("Error encoding FLV tag for recorder", "id", r.id, "error", err)
	}
}
