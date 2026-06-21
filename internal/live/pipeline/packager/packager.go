package packager

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/media"

	"log/slog"

	"github.com/muntader/zaynin-engine/pkg/toolpath"
)

func init() {
	egress.RegisterSink("shaka_packager", NewShakaPackagerSink)
}

type ShakaPackagerSink struct {
	id                 string
	cmd                *exec.Cmd
	wg                 sync.WaitGroup
	closeOnce          sync.Once
	created            time.Time
	liveAdaptiveDir    string
	archiveAdaptiveDir string
	isDvrEnabled       bool
}

func NewShakaPackagerSink(config map[string]interface{}) (egress.Sink, error) {

	stream, ok := config["stream"].(*core.Stream)
	if !ok {
		return nil, errors.New("ffmpeg_transcoder requires a valid 'stream' object in config")
	}

	sessionID, _ := config["sessionID"].(string)
	inputPorts, _ := config["inputPorts"].([]int)
	appConfig, _ := config["appConfig"].(configTypes.Config)
	isDemuxedInput, _ := config["isDemuxedInput"].(bool)

	if sessionID == "" || len(inputPorts) == 0 {
		return nil, fmt.Errorf("shaka_packager config missing required fields")
	}

	liveAdaptiveDir := filepath.Join(appConfig.Storage.Paths.LiveMedia, stream.ID(), sessionID, "adaptive")
	archiveAdaptiveDir := filepath.Join(appConfig.Storage.Paths.LiveArchive, stream.ID(), sessionID, "adaptive")

	args := generateShakaArgs(stream.Config(), inputPorts, liveAdaptiveDir, isDemuxedInput)
	shakaPath, err := toolpath.Resolve("shaka-packager")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(shakaPath, args...)

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create shaka packager stderr pipe: %w", err)
	}

	p := &ShakaPackagerSink{
		id:                 fmt.Sprintf("packager-shaka-%s-%s", stream.ID(), sessionID),
		cmd:                cmd,
		created:            time.Now(),
		liveAdaptiveDir:    liveAdaptiveDir,
		archiveAdaptiveDir: archiveAdaptiveDir,
		wg:                 sync.WaitGroup{},
		closeOnce:          sync.Once{},
		isDvrEnabled:       stream.Config().Pipeline.Package.DvrEnabled,
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.logOutput(p.id, stderrPipe)
	}()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start shaka packager: %w", err)
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := p.cmd.Wait(); err != nil {
			slog.Warn("Shaka packager process exited", "id", p.id, "error", err)
		}
	}()

	return p, nil
}

func (p *ShakaPackagerSink) ID() string { return p.id }

// Close tears down shaka and either archives or deletes segments based on DVR flag.
func (p *ShakaPackagerSink) Close() error {
	p.closeOnce.Do(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		p.wg.Wait()

		if _, err := os.Stat(p.liveAdaptiveDir); os.IsNotExist(err) {
			slog.Warn("Live adaptive directory does not exist, nothing to clean up.", "id", p.id, "directory", p.liveAdaptiveDir)
			return
		}

		if p.isDvrEnabled {
			// keep segments for vod packaging later
			parentArchiveDir := filepath.Dir(p.archiveAdaptiveDir)
			if err := os.MkdirAll(parentArchiveDir, 0755); err != nil {
				slog.Error("Failed to create parent archive directory", "id", p.id, "directory", parentArchiveDir, "error", err)
				return
			}

			if err := os.Rename(p.liveAdaptiveDir, p.archiveAdaptiveDir); err != nil {
				slog.Error("Failed to archive adaptive assets", "id", p.id, "from", p.liveAdaptiveDir, "to", p.archiveAdaptiveDir, "error", err)
			}
		} else {
			// ephemeral live   dont leave segments behind
			if err := os.RemoveAll(p.liveAdaptiveDir); err != nil {
				slog.Error("Failed to delete live adaptive directory", "id", p.id, "directory", p.liveAdaptiveDir, "error", err)
			}
		}
	})
	return nil
}

// WritePacket is a no-op   packager drinks from udp, not the broadcast loop.
func (p *ShakaPackagerSink) WritePacket(packet *media.Packet) error {
	return nil
}

func (p *ShakaPackagerSink) logOutput(streamID string, pipe io.ReadCloser) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		slog.Info("shaka", "stream_id", streamID, "message", scanner.Text())
	}
}

func generateShakaArgs(streamCfg *media.StreamConfig, ports []int, streamOutputDir string, isDemuxedInput bool) []string {
	var args []string
	var streamDescriptors []string
	pkgCfg := streamCfg.Pipeline.Package
	transcodeCfg := streamCfg.Pipeline.Transcode

	const segmentTemplateFormat = "$Number%05d$"
	outputFileExt := "m4s"

	numVideoRenditions := len(transcodeCfg.VideoRenditions)
	numAudioRenditions := len(transcodeCfg.AudioRenditions)

	isAudioOnly := numVideoRenditions == 0 && numAudioRenditions > 0

	portIndex := 0

	if numVideoRenditions > 0 {
		for i := 0; i < numVideoRenditions; i++ {
			if portIndex >= len(ports) {
				slog.Error("Not enough ports for video rendition", "rendition", i, "portIndex", portIndex, "totalPorts", len(ports))
				continue
			}

			port := ports[portIndex]
			portIndex++
			rendition := transcodeCfg.VideoRenditions[i]

			segmentTemplate := fmt.Sprintf("video_%d_%dp_%s.%s", i, rendition.Height, segmentTemplateFormat, outputFileExt)
			playlistName := fmt.Sprintf("video_%d.m3u8", i)

			streamDescriptor := fmt.Sprintf(
				"in=udp://127.0.0.1:%d,format=mpeg2ts,stream=video,segment_template=%s,playlist_name=%s",
				port,
				filepath.Join(streamOutputDir, segmentTemplate),
				playlistName,
			)
			streamDescriptors = append(streamDescriptors, streamDescriptor)
		}
	}

	if numAudioRenditions > 0 {
		if isDemuxedInput {
			for i, rendition := range transcodeCfg.AudioRenditions {
				if portIndex >= len(ports) {
					slog.Error("Not enough ports for audio rendition", "rendition", i, "portIndex", portIndex, "totalPorts", len(ports))
					continue
				}

				port := ports[portIndex]
				portIndex++

				audioSubDir := filepath.Join("audio", rendition.Language)
				fullAudioOutDir := filepath.Join(streamOutputDir, audioSubDir)

				if err := os.MkdirAll(fullAudioOutDir, 0755); err != nil {
					slog.Error("Failed to create audio output directory", "path", fullAudioOutDir, "error", err)
					continue
				}

				segmentTemplate := fmt.Sprintf("audio_%d_%s_%s.%s", i, rendition.Language, segmentTemplateFormat, outputFileExt)
				playlistName := fmt.Sprintf("audio_%d_%s.m3u8", i, rendition.Language)

				streamDescriptor := fmt.Sprintf(
					"in=udp://127.0.0.1:%d,format=mpeg2ts,stream=0,segment_template=%s,lang=%s,playlist_name=%s,hls_group_id=audio,hls_name=%s,dash_role=alternate",
					port,
					filepath.Join(fullAudioOutDir, segmentTemplate),
					rendition.Language,
					filepath.Join(audioSubDir, playlistName),
					rendition.Label,
				)
				streamDescriptors = append(streamDescriptors, streamDescriptor)
			}
		} else {
			if isAudioOnly {
				for i, rendition := range transcodeCfg.AudioRenditions {
					if portIndex >= len(ports) {
						slog.Error("Not enough ports for audio-only rendition", "rendition", i, "portIndex", portIndex, "totalPorts", len(ports))
						continue
					}

					port := ports[portIndex]
					portIndex++

					audioSubDir := filepath.Join("audio", rendition.Language)
					fullAudioOutDir := filepath.Join(streamOutputDir, audioSubDir)

					if err := os.MkdirAll(fullAudioOutDir, 0755); err != nil {
						slog.Error("Failed to create audio output directory", "path", fullAudioOutDir, "error", err)
						continue
					}

					segmentTemplate := fmt.Sprintf("audio_%d_%s_%s.%s", i, rendition.Language, segmentTemplateFormat, outputFileExt)
					playlistName := fmt.Sprintf("audio_%d_%s.m3u8", i, rendition.Language)

					streamDescriptor := fmt.Sprintf(
						"in=udp://127.0.0.1:%d,format=mpeg2ts,stream=audio,segment_template=%s,lang=%s,playlist_name=%s,hls_group_id=audio,hls_name=%s",
						port,
						filepath.Join(fullAudioOutDir, segmentTemplate),
						rendition.Language,
						filepath.Join(audioSubDir, playlistName),
						rendition.Label,
					)
					streamDescriptors = append(streamDescriptors, streamDescriptor)
				}
			} else {
				// muxed video+audio   audio comes off the first video port
				if len(ports) > 0 {
					rendition := transcodeCfg.AudioRenditions[0]
					if len(transcodeCfg.AudioRenditions) > 1 {
						slog.Warn("Multiple audio renditions configured for muxed input, only using the first one")
					}

					audioSubDir := filepath.Join("audio", rendition.Language)
					fullAudioOutDir := filepath.Join(streamOutputDir, audioSubDir)

					if err := os.MkdirAll(fullAudioOutDir, 0755); err != nil {
						slog.Error("Failed to create audio output directory", "path", fullAudioOutDir, "error", err)
					} else {
						segmentTemplate := fmt.Sprintf("audio_0_%s_%s.%s", rendition.Language, segmentTemplateFormat, outputFileExt)
						playlistName := fmt.Sprintf("audio_0_%s.m3u8", rendition.Language)

						streamDescriptor := fmt.Sprintf(
							"in=udp://127.0.0.1:%d,format=mpeg2ts,stream=audio,segment_template=%s,lang=%s,playlist_name=%s,hls_group_id=audio,hls_name=%s",
							ports[0],
							filepath.Join(fullAudioOutDir, segmentTemplate),
							rendition.Language,
							filepath.Join(audioSubDir, playlistName),
							rendition.Label,
						)
						streamDescriptors = append(streamDescriptors, streamDescriptor)
					}
				}
			}
		}
	}

	if len(streamDescriptors) == 0 {
		slog.Error("No stream descriptors generated for Shaka Packager")
		return nil
	}

	args = append(args, streamDescriptors...)

	args = append(args,
		"--v=1",
		fmt.Sprintf("--segment_duration=%d", pkgCfg.SegmentDuration),
		"--minimum_update_period=0",
		"--suggested_presentation_delay=0",
		"--preserved_segments_outside_live_window=0",
	)

	if pkgCfg.LowLatencyEnabled {
		// ll-dash: let shaka infer partials, just flip the mode on
		args = append(args,
			"--low_latency_dash_mode=true",
			"--utc_timings=urn:mpeg:dash:utc:http-xsdate:2014",
		)
	} else {
		args = append(args,
			fmt.Sprintf("--fragment_duration=%.1f", pkgCfg.FragmentDuration),
			"--generate_static_live_mpd",
		)
	}

	if pkgCfg.DvrEnabled && pkgCfg.TimeShiftBufferDepth > 0 {
		args = append(args, fmt.Sprintf("--time_shift_buffer_depth=%d", pkgCfg.TimeShiftBufferDepth))
	} else {
		args = append(args, "--time_shift_buffer_depth=0")
	}

	if pkgCfg.EnableHls {
		args = append(args,
			"--hls_playlist_type=LIVE",
			fmt.Sprintf("--hls_master_playlist_output=%s", filepath.Join(streamOutputDir, "master.m3u8")),
		)
	}
	if pkgCfg.EnableDash {
		args = append(args, fmt.Sprintf("--mpd_output=%s", filepath.Join(streamOutputDir, "manifest.mpd")))
	}

	return args
}
