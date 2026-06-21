package transcoder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/hardware"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/media"
	"github.com/muntader/zaynin-engine/pkg/toolpath"
	"github.com/sirupsen/logrus"
	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"
)

// registered at init so core doesnt import ffmpeg directly
func init() {
	egress.RegisterSink("ffmpeg_transcoder", NewFFmpegTranscoder)
}

// FFmpegTranscoder supervises one ffmpeg child   same pattern as the flv recorder.
type FFmpegTranscoder struct {
	id     string
	config map[string]interface{}
	stream *core.Stream

	packetChan chan *media.Packet
	stopChan   chan struct{}
	wg         sync.WaitGroup
	closeOnce  sync.Once

	// for ops dashboards
	packetsIn      atomic.Int64
	packetsDropped atomic.Int64
}

func NewFFmpegTranscoder(config map[string]interface{}) (egress.Sink, error) {
	stream, ok := config["stream"].(*core.Stream)
	if !ok {
		return nil, errors.New("ffmpeg_transcoder requires a valid 'stream' object in config")
	}

	t := &FFmpegTranscoder{
		id:         fmt.Sprintf("transcoder-%s", stream.ID()),
		config:     config,
		stream:     stream,
		packetChan: make(chan *media.Packet, 2048),
		stopChan:   make(chan struct{}),
	}

	// supervisor starts immediately   WritePacket should never block on setup
	t.wg.Add(1)
	go t.supervisor()

	return t, nil
}

func (t *FFmpegTranscoder) ID() string { return t.id }

// WritePacket is non-blocking   drops if we're shutting down or full.
func (t *FFmpegTranscoder) WritePacket(p *media.Packet) error {

	select {
	case t.packetChan <- p:
	case <-t.stopChan:
	default:
		//dropped := t.packetsDropped.Add(1)
		//if dropped%10 == 1 { // Log every 10th drop to avoid spam
		//	logrus.Warnf("[%s] Dropping packet, channel full. Total dropped: %d", t.id, dropped)
		//}
	}
	return nil
}

// Close signals the supervisor to shut down all operations and waits for completion.
func (t *FFmpegTranscoder) Close() error {
	t.closeOnce.Do(func() {
		logrus.Infof("[%s] Closing transcoder. Total packets received: %d, Dropped: %d",
			t.id, t.packetsIn.Load(), t.packetsDropped.Load())

		close(t.stopChan)
		t.wg.Wait()

		logrus.Infof("[%s] Transcoder closed.", t.id)
	})
	return nil
}

// supervisor waits for headers/properties, then runs ffmpeg with restarts.
func (t *FFmpegTranscoder) supervisor() {
	defer t.wg.Done()

	sourceFormat := t.stream.Source().Format()
	logrus.Infof("[%s] Supervisor starting for source format: %s", t.id, sourceFormat)

	initialPackets := make([]*media.Packet, 0, 256)
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()

	// phase 1: buffer until we know enough to start ffmpeg
	var hasMetadata, hasAudioHeader, hasVideoHeader bool
	var hasSeenAudio, hasSeenVideo bool
	if sourceFormat == "flv" {
		detectionTimeout := time.NewTimer(3 * time.Second)
		detectionComplete := false

		for {
			allRequiredHeadersReceived := hasMetadata && (!hasSeenAudio || hasAudioHeader) && (!hasSeenVideo || hasVideoHeader)
			if detectionComplete && allRequiredHeadersReceived {
				break
			}
			select {
			case packet := <-t.packetChan:
				initialPackets = append(initialPackets, packet)
				switch packet.Type {
				case media.PacketTypeMetadata:
					hasMetadata = true
				case media.PacketTypeAudio:
					hasSeenAudio = true
					if packet.IsSequenceHeader {
						hasAudioHeader = true
					}
				case media.PacketTypeVideo:
					hasSeenVideo = true
					if packet.IsSequenceHeader {
						hasVideoHeader = true
					}
				}
			case <-detectionTimeout.C:
				detectionComplete = true
			case <-timeout.C:
				logrus.Errorf("[%s] Supervisor timed out waiting for FLV stream headers.", t.id)
				go t.stream.Stop()
				return
			case <-t.stopChan:
				logrus.Infof("[%s] Supervisor shutting down while waiting for FLV headers.", t.id)
				return
			}
		}
		logrus.Infof("[%s] FLV header collection complete.", t.id)
	} else if sourceFormat == "mpegts" {
		// mpegts: ffprobe already told us the tracks, just buffer some data
		logrus.Infof("[%s] Waiting for stream properties from ffprobe...", t.id)
		propertiesReady := false
		for !propertiesReady {
			select {
			case packet := <-t.packetChan:
				initialPackets = append(initialPackets, packet)
			case <-time.After(50 * time.Millisecond):
				if t.stream.Properties().IsReady() {
					propertiesReady = true
				}
			case <-timeout.C:
				logrus.Errorf("[%s] Supervisor timed out waiting for MPEG-TS stream properties.", t.id)
				go t.stream.Stop()
				return
			case <-t.stopChan:
				logrus.Infof("[%s] Supervisor shutting down while waiting for MPEG-TS properties.", t.id)
				return
			}
		}
		logrus.Infof("[%s] MPEG-TS stream properties are ready. Buffered %d initial packets.", t.id, len(initialPackets))
	} else {
		logrus.Errorf("[%s] Supervisor encountered unsupported source format: %s", t.id, sourceFormat)
		go t.stream.Stop()
		return
	}

	err := t.stream.Properties().Wait(15 * time.Second)
	if err != nil {
		logrus.Errorf("[%s] Supervisor timed out waiting for stream properties: %v", t.id, err)
		go t.stream.Stop()
		return
	}

	logrus.Infof("[%s] Supervisor: Stream properties are ready.", t.id)
	props := t.stream.Properties()

	// Log the rich information we got from ffprobe
	logrus.Infof("[%s] -> Detected Properties: %d video tracks, %d audio tracks.", t.id, len(props.GetVideoStreams()), props.NumAudioTracks())
	if len(props.GetVideoStreams()) > 0 {
		videoInfo := props.GetVideoStreams()[0]
		logrus.Infof("[%s] -> Video Details: %dx%d @ %.2f FPS, Codec: %s", t.id, videoInfo.Width, videoInfo.Height, videoInfo.FrameRate, videoInfo.CodecName)
	}
	if props.NumAudioTracks() > 0 {
		logrus.Infof("[%s] -> Audio Details: %d tracks detected", t.id, props.NumAudioTracks())
	}

	// dont trust config audio tracks past what ffprobe saw
	streamConfig := t.stream.Config()
	var validAudioRenditions []media.AudioRenditionConfig
	detectedAudioTracks := props.NumAudioTracks()

	if detectedAudioTracks > 0 {
		for _, requestedAudio := range streamConfig.Pipeline.Transcode.AudioRenditions {
			if requestedAudio.InputTrackIndex < detectedAudioTracks {
				validAudioRenditions = append(validAudioRenditions, requestedAudio)
			} else {
				logrus.Warnf("[%s] Filtering out requested audio track that does not exist in source. Requested index: %d, Detected tracks: %d",
					t.id, requestedAudio.InputTrackIndex, detectedAudioTracks)
			}
		}
	} else {
		logrus.Infof("[%s] No audio tracks detected in stream, skipping audio renditions", t.id)
	}

	streamConfig.Pipeline.Transcode.AudioRenditions = validAudioRenditions
	logrus.Infof("[%s] Config sanitized. Proceeding with %d valid audio renditions.", t.id, len(validAudioRenditions))

	resManager, ok := t.config["resourceManager"].(*hardware.ResourceManager)
	if !ok {
		logrus.Errorf("[%s] Supervisor: ResourceManager not found in config.", t.id)
		go t.stream.Stop()
		return
	}

	// bail early if the box is already pegged
	status, err := resManager.CheckCapacity()
	if err != nil {
		logrus.Errorf("[%s] Supervisor: Cannot start transcode, worker at capacity: %v", t.id, err)
		go t.stream.Stop()
		return
	}
	logrus.Infof("[%s] Supervisor: System capacity check passed. Current CPU: %.1f%%", t.id, status.CPUUsagePercent)

	assignedGPUID := -1
	pref := streamConfig.Pipeline.Transcode.HardwarePreference
	if pref != "force_cpu" {
		assignedGPUID = status.FindLeastLoadedGPU()
		if assignedGPUID != -1 {
			logrus.Infof("[%s] Placing transcode on least-loaded GPU: %d", t.id, assignedGPUID)
		} else {
			logrus.Infof("[%s] No suitable GPU found, falling back to CPU.", t.id)
		}
	}
	if pref == "force_gpu" && assignedGPUID == -1 {
		logrus.Errorf("[%s] GPU placement was forced, but no suitable GPU is available.", t.id)
		go t.stream.Stop()
		return
	}

	maxRestarts := 3
	for restartCount := 0; restartCount < maxRestarts; restartCount++ {
		select {
		case <-t.stopChan:
			logrus.Infof("[%s] Supervisor shutting down before FFmpeg start.", t.id)
			return
		default:
		}

		err := t.runFFmpegInstance(assignedGPUID, initialPackets)
		initialPackets = nil

		if err == nil {
			logrus.Infof("[%s] Supervisor: FFmpeg exited cleanly. Sink is finished.", t.id)
			return
		}

		select {
		case <-t.stopChan:
			logrus.Infof("[%s] Supervisor shutting down after FFmpeg exit.", t.id)
			return
		default:
		}
		logrus.Warnf("[%s] Supervisor: FFmpeg process crashed (error: %v). Restart count: %d", t.id, err, restartCount+1)
	}

	logrus.Errorf("[%s] Supervisor: Maximum restart limit reached. Stopping stream.", t.id)
	t.stream.Stop()
}

// runFFmpegInstance owns one ffmpeg process lifetime   pipes must drain or we deadlock.
func (t *FFmpegTranscoder) runFFmpegInstance(assignedGPUID int, initialPackets []*media.Packet) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-t.stopChan
		cancel()
	}()

	streamConfig := t.stream.Config()
	ports, ok := t.config["outputPorts"].([]int)
	if !ok || len(ports) == 0 {
		err := errors.New("transcoder did not receive valid 'outputPorts' in config")
		return err
	}

	appConfig := t.config["appConfig"].(types.Config)
	logDir := filepath.Join(appConfig.Storage.Paths.LiveMedia, "ffmpeg")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		logrus.Errorf("[%s] Failed to create FFmpeg log directory: %v", t.id, err)
	}
	logFilePath := filepath.Join(logDir, fmt.Sprintf("%s-%d.log", t.stream.ID(), time.Now().Unix()))
	logFile, err := os.Create(logFilePath)
	if err != nil {
		logrus.Errorf("[%s] Failed to create FFmpeg log file, will not log to file: %v", t.id, err)
	} else {
		logrus.Infof("[%s] Logging FFmpeg output to: %s", t.id, logFilePath)
	}

	isDemuxed, _ := t.config["useDemuxedOutput"].(bool)
	sourceFormat := t.stream.Source().Format()

	args := GenerateFFmpegArgs(
		streamConfig,
		t.stream.Properties(),
		ports,
		t.stream.ID(),
		t.config["sessionID"].(string),
		t.config["appConfig"].(types.Config),
		sourceFormat,
		assignedGPUID,
		isDemuxed,
	)

	if args == nil {
		return errors.New("failed to generate FFmpeg args")
	}

	ffmpegPath, err := toolpath.Resolve("ffmpeg")
	if err != nil {
		return err
	}
	command := "stdbuf"
	stdbufArgs := []string{"-oL", "-eL", ffmpegPath}
	finalArgs := append(stdbufArgs, args...)

	logrus.Infof("[%s] Starting FFmpeg with command: %s %s", t.id, command, strings.Join(finalArgs, " "))
	cmd := exec.CommandContext(ctx, command, finalArgs...)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	// file logging   no need for goroutines reading pipes
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	var flvEnc *flv.Encoder
	if sourceFormat == "flv" {
		flvEnc, _ = flv.NewEncoder(stdinPipe, flv.FlagsAudio|flv.FlagsVideo)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg process: %w", err)
	}

	if logFile != nil {
		logFile.Close()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdinPipe.Close()

		for _, packet := range initialPackets {
			var err error
			if sourceFormat == "flv" {
				err = t.pipeFlvPacket(flvEnc, packet)
			} else {
				_, err = stdinPipe.Write(packet.Data)
			}
			if err != nil {
				logrus.Warnf("[%s] Error writing initial packet to FFmpeg, aborting pipe: %v", t.id, err)
				return
			}
		}

		logrus.Infof("[%s] Now forwarding new %s packets to FFmpeg...", t.id, strings.ToUpper(sourceFormat))
		for {
			select {
			case packet, ok := <-t.packetChan:
				if !ok {
					return
				}

				var err error
				if sourceFormat == "flv" {
					err = t.pipeFlvPacket(flvEnc, packet)
				} else {
					_, err = stdinPipe.Write(packet.Data)
				}
				if err != nil {
					// ffmpeg probably exited and closed stdin
					logrus.Warnf("[%s] Error writing packet to FFmpeg, stopping pipe: %v", t.id, err)
					return
				}

			case <-ctx.Done():
				return
			}
		}
	}()

	processErr := cmd.Wait()
	wg.Wait()

	if processErr != nil {
		logrus.Errorf("[%s] FFmpeg process exited with error: %v. Check log file for details: %s", t.id, processErr, logFilePath)
	} else {
		logrus.Infof("[%s] FFmpeg process exited cleanly.", t.id)
	}

	return processErr
}

func (t *FFmpegTranscoder) pipeFlvPacket(flvEnc *flv.Encoder, packet *media.Packet) error {
	var flvTag flvtag.FlvTag
	timestamp := uint32(packet.Timestamp / time.Millisecond)

	switch packet.Type {
	case media.PacketTypeMetadata:
		var script flvtag.ScriptData
		if err := flvtag.DecodeScriptData(bytes.NewReader(packet.Data), &script); err != nil {
			return fmt.Errorf("failed to decode script data: %w", err)
		}
		flvTag.TagType = flvtag.TagTypeScriptData
		flvTag.Data = &script
	case media.PacketTypeAudio:
		var audio flvtag.AudioData
		if err := flvtag.DecodeAudioData(bytes.NewReader(packet.Data), &audio); err != nil {
			return fmt.Errorf("failed to decode audio data: %w", err)
		}
		flvTag.TagType = flvtag.TagTypeAudio
		flvTag.Timestamp = timestamp
		flvTag.Data = &audio
	case media.PacketTypeVideo:
		var video flvtag.VideoData
		if err := flvtag.DecodeVideoData(bytes.NewReader(packet.Data), &video); err != nil {
			return fmt.Errorf("failed to decode video data: %w", err)
		}
		flvTag.TagType = flvtag.TagTypeVideo
		flvTag.Timestamp = timestamp
		flvTag.Data = &video
	default:
		return nil
	}
	return flvEnc.Encode(&flvTag)
}

func (t *FFmpegTranscoder) logPipe(pipe io.ReadCloser, pipeName string) {
	defer pipe.Close()
	logrus.Infof("[%s] Started logging FFmpeg %s.", t.id, pipeName)
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		logrus.Infof("[ffmpeg-%s]: %s", t.id, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		logrus.Errorf("[%s] Error reading from FFmpeg %s pipe: %v", t.id, pipeName, err)
	}
	logrus.Infof("[%s] Finished logging FFmpeg %s.", t.id, pipeName)
}

func isGPUError(err error) bool {
	return err != nil && err.Error() != "exit status 1"
}

func generateDynamicRenditions(sourceWidth, sourceHeight int) []media.VideoRenditionConfig {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return []media.VideoRenditionConfig{}
	}
	aspectRatio := float64(sourceWidth) / float64(sourceHeight)
	type LadderEntry struct {
		Height       int
		VideoBitrate int
		AudioBitrate int
	}

	professionalLadder := []LadderEntry{
		{Height: 1440, VideoBitrate: 12000000, AudioBitrate: 192000},
		{Height: 1440, VideoBitrate: 8000000, AudioBitrate: 192000},
		{Height: 1080, VideoBitrate: 4000000, AudioBitrate: 128000},
		{Height: 720, VideoBitrate: 2200000, AudioBitrate: 128000},
		{Height: 480, VideoBitrate: 900000, AudioBitrate: 96000},
		{Height: 360, VideoBitrate: 600000, AudioBitrate: 96000},
	}
	var generatedLadder []media.VideoRenditionConfig
	sourceBitrate := int(float64(sourceWidth*sourceHeight) * 2.0 / 1000.0)
	if sourceBitrate > 25000 {
		sourceBitrate = 25000
	}
	generatedLadder = append(generatedLadder, media.VideoRenditionConfig{
		Height: sourceHeight, Width: sourceWidth, VideoBitrate: sourceBitrate})
	lastAddedHeight := sourceHeight
	for _, entry := range professionalLadder {
		if entry.Height < lastAddedHeight {
			newWidth := int(float64(entry.Height) * aspectRatio)
			if newWidth%2 != 0 {
				newWidth++
			}
			generatedLadder = append(generatedLadder, media.VideoRenditionConfig{
				Height: entry.Height, Width: newWidth, VideoBitrate: entry.VideoBitrate,
			})
			lastAddedHeight = entry.Height
		}
	}
	return generatedLadder
}
