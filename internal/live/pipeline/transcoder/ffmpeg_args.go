package transcoder

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/media"

	"strings"
)

func BtoI(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func calculateVbvParams(bitrateKbps int) (maxrateKbps, bufsizeKbps int) {
	// keeps encoders VBV-happy for players that care
	maxrateKbps = int(float64(bitrateKbps) * 1.2)
	bufsizeKbps = maxrateKbps * 2
	return
}

// determineEncoderName maps our abstract codec + gpu flag to an ffmpeg encoder.
func determineEncoderName(codec string, useGPU bool) (string, string) {
	switch codec {
	case "h264":
		if useGPU {
			return "h264_nvenc", ""
		}
		return "libx264", ""
	case "hevc":
		// hvc1 helps on apple devices
		if useGPU {
			return "hevc_nvenc", "hvc1"
		}
		return "libx265", "hvc1"
	case "av1":
		if useGPU {
			return "av1_nvenc", ""
		}
		return "libaom-av1", ""
	default:
		slog.Warn("Unsupported codec specified, falling back to libx264", "codec", codec)
		return "libx264", ""
	}
}

func getDefaultEncoderParams(encoderName string) media.EncoderParams {
	pStr := func(s string) *string { return &s }
	pInt := func(i int) *int { return &i }
	pBool := func(b bool) *bool { return &b }

	if strings.Contains(encoderName, "_nvenc") {
		// nvenc defaults   balanced for live
		return media.EncoderParams{
			Preset:     pStr("p6"),
			Tune:       pStr("hq"),
			Profile:    pStr("high"),
			Bframes:    pInt(2),
			Refs:       pInt(3),
			Lookahead:  pInt(16),
			TemporalAQ: pBool(true),
		}
	} else if encoderName == "libx264" {
		// x264 needs to be fast or we fall behind realtime
		return media.EncoderParams{
			Preset:  pStr("fast"),
			Tune:    pStr("zerolatency"),
			Profile: pStr("high"),
			Level:   pStr("4.1"),
			Bframes: pInt(2),
			Refs:    pInt(2),
		}
	} else if encoderName == "libx265" {
		// hevc on cpu is brutal   fast or bust
		return media.EncoderParams{
			Preset:  pStr("fast"),
			Tune:    pStr("zerolatency"),
			Bframes: pInt(2),
		}
	}

	// fallback when we dont recognize the encoder
	return media.EncoderParams{}
}

// GenerateFFmpegArgs builds the full ffmpeg argv for ABR or passthrough.
func GenerateFFmpegArgs(
	streamConfig *media.StreamConfig,
	properties *core.StreamProperties,
	ports []int,
	streamID string,
	sessionID string,
	appConfig types.Config,
	inputFormat string,
	assignedGPUID int,
	useDemuxedOutput bool,
) []string {
	transcodeCfg := streamConfig.Pipeline.Transcode
	packageCfg := streamConfig.Pipeline.Package
	videoStreams := properties.GetVideoStreams()
	hasVideoStream := len(videoStreams) > 0
	hasAudioStream := properties.NumAudioTracks() > 0

	if !transcodeCfg.Enabled {
		liveAdaptiveDir := filepath.Join(appConfig.Storage.Paths.LiveMedia, streamID, sessionID, "adaptive")
		if err := os.MkdirAll(liveAdaptiveDir, 0755); err != nil {
			slog.Error("FATAL: Failed to create HLS output directory for passthrough", "directory", liveAdaptiveDir, "error", err)
			return nil
		}

		args := []string{
			"-hide_banner", "-loglevel", "info", "-stats",
			"-analyzeduration", "100000",
			"-probesize", "100000",
			"-fflags", "+genpts+igndts+flush_packets",
			"-avoid_negative_ts", "make_zero",
			"-max_delay", "100000",
			"-thread_queue_size", "1024",
			"-f", inputFormat, "-i", "pipe:0",
		}

		if packageCfg.LowLatencyEnabled {
			segmentDuration := packageCfg.SegmentDuration
			if segmentDuration <= 0 {
				segmentDuration = 1
			}

			fragmentDuration := packageCfg.FragmentDuration
			if fragmentDuration <= 0 {
				fragmentDuration = 0.2
			}

			args = append(args, "-c:a", "copy")

			if hasVideoStream {
				sourceFPS := 30.0
				if videoStreams[0].FrameRate > 0 {
					sourceFPS = videoStreams[0].FrameRate
				}
				gopSize := int(sourceFPS * float64(segmentDuration))

				args = append(args,
					"-c:v", "libx264",
					"-preset", "ultrafast",
					"-tune", "zerolatency",
					"-profile:v", "baseline",
					"-level", "3.1",
					"-x264-params", "nal-hrd=cbr:force-cfr=1:keyint="+fmt.Sprintf("%d", gopSize),
					"-g", fmt.Sprintf("%d", gopSize),
					"-keyint_min", fmt.Sprintf("%d", gopSize),
					"-sc_threshold", "0",
					"-bf", "0",
					"-refs", "1",
					"-slices", "4",
				)
			}

			args = append(args,
				"-map", "0",
				"-f", "hls",
				"-hls_time", fmt.Sprintf("%d", segmentDuration),
				"-hls_list_size", "10",
				"-hls_playlist_type", "event",
				"-hls_segment_type", "mpegts",
				"-hls_flags", "independent_segments+program_date_time+temp_file",
				"-hls_segment_filename", filepath.Join(liveAdaptiveDir, "seg_%06d.ts"),
				filepath.Join(liveAdaptiveDir, "index.m3u8"),
			)

		} else {
			segmentDuration := packageCfg.SegmentDuration
			if segmentDuration <= 0 {
				segmentDuration = 4
			}
			args = append(args,
				"-c", "copy",
				"-map", "0",
				"-f", "hls",
				"-hls_time", fmt.Sprintf("%d", segmentDuration),
				"-hls_list_size", "10",
				"-hls_flags", "delete_segments+program_date_time",
				"-hls_segment_type", "mpegts",
				"-hls_segment_filename", filepath.Join(liveAdaptiveDir, "seg%06d.ts"),
				filepath.Join(liveAdaptiveDir, "master.m3u8"),
			)
		}

		fmt.Println("--- Generated FFmpeg Low-Latency Command ---")
		fmt.Println("ffmpeg", strings.Join(args, " "))
		fmt.Println("------------------------------------------")
		return args
	}

	if !hasVideoStream && !hasAudioStream {
		slog.Error("Cannot transcode: no video or audio streams detected by ffprobe.", "streamID", streamID)
		return nil
	}

	var sourceFPS float64 = 30.0
	var gopSize int = 100

	if hasVideoStream {
		sourceFPS = videoStreams[0].FrameRate
		if sourceFPS <= 0 {
			slog.Warn("Source framerate detected as 0 or less. Falling back to default framerate (30 fps).", "streamID", streamID)
			sourceFPS = 30.0
		}
		keyframeIntervalSecs := packageCfg.SegmentDuration
		if keyframeIntervalSecs <= 0 {
			keyframeIntervalSecs = 4
		}
		gopSize = int(sourceFPS * float64(keyframeIntervalSecs))
	}

	// Validate ports based on stream content
	numVideoRenditions := len(transcodeCfg.VideoRenditions)
	numAudioRenditions := len(transcodeCfg.AudioRenditions)

	if !hasVideoStream && numVideoRenditions > 0 {
		slog.Warn("Video renditions requested but no video stream detected, removing video renditions", "streamID", streamID)
		transcodeCfg.VideoRenditions = []media.VideoRenditionConfig{}
		numVideoRenditions = 0
	}

	if !hasAudioStream && numAudioRenditions > 0 {
		slog.Warn("Audio renditions requested but no audio stream detected, removing audio renditions", "streamID", streamID)
		transcodeCfg.AudioRenditions = []media.AudioRenditionConfig{}
		numAudioRenditions = 0
	}

	var numRequiredPorts int
	if useDemuxedOutput {
		numRequiredPorts = numVideoRenditions + numAudioRenditions
	} else {
		// muxed: one udp port per video rung (audio rides along)
		if numVideoRenditions > 0 {
			numRequiredPorts = numVideoRenditions
		} else {
			numRequiredPorts = numAudioRenditions
		}
	}

	if len(ports) < numRequiredPorts {
		slog.Error("Not enough UDP ports provided for the selected pipeline strategy",
			"required", numRequiredPorts, "provided", len(ports), "isDemuxed", useDemuxedOutput,
			"hasVideo", hasVideoStream, "hasAudio", hasAudioStream)
		return nil
	}

	args := []string{"-hide_banner", "-loglevel", "info", "-stats", "-threads", "0"}

	// srt mpegts often has dodgy timestamps   be more forgiving on probe
	if inputFormat == "mpegts" {
		args = append(args,
			"-analyzeduration", "1M",
			"-probesize", "1M",
			"-fflags", "+genpts+igndts",
			"-avoid_negative_ts", "make_zero",
			"-max_delay", "500000")
	} else {
		args = append(args, "-analyzeduration", "2M", "-probesize", "2M")
	}

	useGPU := assignedGPUID != -1 && hasVideoStream
	if useGPU {
		args = append(args, "-hwaccel", "cuda", "-hwaccel_device", fmt.Sprintf("%d", assignedGPUID), "-hwaccel_output_format", "cuda")
	}
	args = append(args, "-f", inputFormat, "-i", "pipe:0")

	// Build filter complex only if we have renditions to process
	if numVideoRenditions > 0 || numAudioRenditions > 0 {
		filterComplex, videoOutputs, audioOutputs := buildMultiStreamFilterComplex(streamConfig, properties, useGPU, useDemuxedOutput)
		if filterComplex == "" {
			slog.Error("Failed to build a valid filter complex.", "streamID", streamID)
			return nil
		}
		args = append(args, "-filter_complex", filterComplex)

		portIndex := 0

		if useDemuxedOutput {
			if numVideoRenditions > 0 {
				encoderName, codecTag := determineEncoderName(transcodeCfg.Codec, useGPU)
				for i, r := range transcodeCfg.VideoRenditions {
					udpURL := fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316&fifo_size=5000000", ports[portIndex])
					portIndex++

					videoArgs := buildVideoArgs(r, encoderName, codecTag, gopSize, assignedGPUID)
					outputArgs := []string{"-map", videoOutputs[i]}
					outputArgs = append(outputArgs, videoArgs...)
					if transcodeCfg.Codec == "hevc" {
						outputArgs = append(outputArgs, "-bsf:v", "hevc_mp4toannexb")
					}
					outputArgs = append(outputArgs, "-f", "mpegts", udpURL)
					args = append(args, outputArgs...)
				}
			}

			if numAudioRenditions > 0 {
				for i, r := range transcodeCfg.AudioRenditions {
					udpURL := fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316&fifo_size=5000000", ports[portIndex])
					portIndex++

					audioArgs := []string{
						"-c:a", r.Codec,
						"-b:a", fmt.Sprintf("%dk", r.Bitrate),
						"-ac", "2",
					}
					outputArgs := []string{"-map", audioOutputs[i]}
					outputArgs = append(outputArgs, audioArgs...)
					outputArgs = append(outputArgs, "-f", "mpegts", udpURL)
					args = append(args, outputArgs...)
				}
			}
		} else {
			if numVideoRenditions > 0 {
				encoderName, codecTag := determineEncoderName(transcodeCfg.Codec, useGPU)
				for i, videoRendition := range transcodeCfg.VideoRenditions {
					udpURL := fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316&fifo_size=5000000", ports[i])
					var outputArgs []string

					outputArgs = append(outputArgs, "-map", videoOutputs[i])

					if hasAudioStream && i < len(audioOutputs) {
						outputArgs = append(outputArgs, "-map", audioOutputs[i])
					}

					videoArgs := buildVideoArgs(videoRendition, encoderName, codecTag, gopSize, assignedGPUID)
					outputArgs = append(outputArgs, videoArgs...)

					if hasAudioStream && len(transcodeCfg.AudioRenditions) > 0 {
						audioRendition := transcodeCfg.AudioRenditions[0]
						outputArgs = append(outputArgs,
							"-c:a", audioRendition.Codec,
							"-b:a", fmt.Sprintf("%dk", audioRendition.Bitrate),
							"-ac", "2",
						)
					}

					outputArgs = append(outputArgs, "-f", "mpegts", udpURL)
					args = append(args, outputArgs...)
				}
			} else if numAudioRenditions > 0 {
				// audio-only muxed path
				for i, audioRendition := range transcodeCfg.AudioRenditions {
					udpURL := fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316&fifo_size=5000000", ports[i])
					var outputArgs []string

					outputArgs = append(outputArgs, "-map", audioOutputs[i])

					outputArgs = append(outputArgs,
						"-c:a", audioRendition.Codec,
						"-b:a", fmt.Sprintf("%dk", audioRendition.Bitrate),
						"-ac", "2",
					)

					outputArgs = append(outputArgs, "-f", "mpegts", udpURL)
					args = append(args, outputArgs...)
				}
			}
		}
	} else {
		slog.Error("No renditions to process after filtering", "streamID", streamID)
		return nil
	}

	fmt.Println("--- Generated FFmpeg Command ---")
	fmt.Println("ffmpeg", strings.Join(args, " "))
	fmt.Println("------------------------------")
	return args
}

func buildMultiStreamFilterComplex(
	streamConfig *media.StreamConfig,
	properties *core.StreamProperties,
	useGPU bool,
	useDemuxedOutput bool,
) (string, []string, []string) {
	transcodeCfg := streamConfig.Pipeline.Transcode
	var filterParts, videoOutputs, audioOutputs []string

	videoStreams := properties.GetVideoStreams()
	numVideoRenditions := len(transcodeCfg.VideoRenditions)
	numAudioRenditions := len(transcodeCfg.AudioRenditions)
	numInputAudioTracks := properties.NumAudioTracks()

	hasVideoStream := len(videoStreams) > 0
	hasAudioStream := numInputAudioTracks > 0

	if numVideoRenditions > 0 && hasVideoStream {
		var videoBuilder strings.Builder
		fmt.Fprintf(&videoBuilder, "[0:v:0]split=%d", numVideoRenditions)
		for i := 0; i < numVideoRenditions; i++ {
			fmt.Fprintf(&videoBuilder, "[v%d]", i)
			videoOutputs = append(videoOutputs, fmt.Sprintf("[v%d_out]", i))
		}
		filterParts = append(filterParts, videoBuilder.String())

		sourceFPS := 30.0
		if videoStreams[0].FrameRate > 0 {
			sourceFPS = videoStreams[0].FrameRate
		}

		for i, r := range transcodeCfg.VideoRenditions {
			fps := sourceFPS
			if r.Framerate > 0 {
				fps = float64(r.Framerate)
			}

			scaleFilter := "scale"
			if useGPU {
				scaleFilter = "scale_cuda"
			}
			filterParts = append(filterParts, fmt.Sprintf("[v%d]fps=%f,%s=%d:%d%s", i, fps, scaleFilter, r.Width, r.Height, videoOutputs[i]))
		}
	}

	if numAudioRenditions > 0 && hasAudioStream {
		// one audio track, many video rungs, muxed   split audio to match
		isMuxedSingleAudioSource := numAudioRenditions == 1 && numVideoRenditions > 1 && !useDemuxedOutput

		if isMuxedSingleAudioSource {
			firstAudioRendition := transcodeCfg.AudioRenditions[0]
			if firstAudioRendition.InputTrackIndex >= numInputAudioTracks {
				slog.Error("Audio rendition requests an input track that does not exist", "requestedIndex", firstAudioRendition.InputTrackIndex, "availableTracks", numInputAudioTracks)
				return "", nil, nil
			}

			inputStream := fmt.Sprintf("[0:a:%d]", firstAudioRendition.InputTrackIndex)
			resampledStream := "[a_resampled]"
			filterParts = append(filterParts, fmt.Sprintf("%saresample=48000%s", inputStream, resampledStream))

			var audioSplitBuilder strings.Builder
			fmt.Fprintf(&audioSplitBuilder, "%sasplit=%d", resampledStream, numVideoRenditions)
			for i := 0; i < numVideoRenditions; i++ {
				outputStream := fmt.Sprintf("[a%d]", i)
				fmt.Fprintf(&audioSplitBuilder, "%s", outputStream)
				audioOutputs = append(audioOutputs, outputStream)
			}
			filterParts = append(filterParts, audioSplitBuilder.String())

		} else {
			for i, r := range transcodeCfg.AudioRenditions {
				if r.InputTrackIndex >= numInputAudioTracks {
					slog.Warn("Skipping audio rendition as its input track index is out of bounds", "renditionIndex", i, "requestedIndex", r.InputTrackIndex, "availableTracks", numInputAudioTracks)
					continue
				}
				inputStream := fmt.Sprintf("[0:a:%d]", r.InputTrackIndex)
				outputStream := fmt.Sprintf("[a%d_out]", i)
				audioOutputs = append(audioOutputs, outputStream)
				filterParts = append(filterParts, fmt.Sprintf("%saresample=48000%s", inputStream, outputStream))
			}
		}
	}

	if len(filterParts) == 0 {
		slog.Warn("No filter complex parts generated - no valid streams to process")
		return "", nil, nil
	}

	return strings.Join(filterParts, ";"), videoOutputs, audioOutputs
}

// buildVideoArgs merges system defaults with per-rendition user overrides.
func buildVideoArgs(r media.VideoRenditionConfig, encoderName, codecTag string, gopSize, assignedGPUID int) []string {
	// gop/keyint tied to segment length   non-negotiable for hls
	maxrateKbps, bufsizeKbps := calculateVbvParams(r.VideoBitrate)
	args := []string{
		"-c:v", encoderName,
		"-g", fmt.Sprintf("%d", gopSize),
		"-keyint_min", fmt.Sprintf("%d", gopSize),
		"-sc_threshold", "0",
		"-b:v", fmt.Sprintf("%d", r.VideoBitrate),
		"-maxrate", fmt.Sprintf("%d", maxrateKbps),
		"-bufsize", fmt.Sprintf("%d", bufsizeKbps),
	}

	if codecTag != "" {
		args = append(args, "-tag:v", codecTag)
	}

	defaults := getDefaultEncoderParams(encoderName)
	userParams := r.Params

	// user wins when they set something, otherwise fall back to defaults
	finalParams := media.EncoderParams{
		Preset:     userParams.Preset,
		Tune:       userParams.Tune,
		Profile:    userParams.Profile,
		Level:      userParams.Level,
		Bframes:    userParams.Bframes,
		Refs:       userParams.Refs,
		Lookahead:  userParams.Lookahead,
		TemporalAQ: userParams.TemporalAQ,
	}
	if finalParams.Preset == nil {
		finalParams.Preset = defaults.Preset
	}
	if finalParams.Tune == nil {
		finalParams.Tune = defaults.Tune
	}
	if finalParams.Profile == nil {
		finalParams.Profile = defaults.Profile
	}
	if finalParams.Level == nil {
		finalParams.Level = defaults.Level
	}
	if finalParams.Bframes == nil {
		finalParams.Bframes = defaults.Bframes
	}
	if finalParams.Refs == nil {
		finalParams.Refs = defaults.Refs
	}
	if finalParams.Lookahead == nil {
		finalParams.Lookahead = defaults.Lookahead
	}
	if finalParams.TemporalAQ == nil {
		finalParams.TemporalAQ = defaults.TemporalAQ
	}

	if finalParams.Preset != nil {
		args = append(args, "-preset", *finalParams.Preset)
	}
	if finalParams.Tune != nil {
		args = append(args, "-tune", *finalParams.Tune)
	}
	if finalParams.Profile != nil {
		args = append(args, "-profile:v", *finalParams.Profile)
	}
	if finalParams.Level != nil {
		args = append(args, "-level:v", *finalParams.Level)
	}

	if strings.Contains(encoderName, "_nvenc") {
		args = append(args, "-rc:v", "vbr", "-gpu", fmt.Sprintf("%d", assignedGPUID))
		if finalParams.Lookahead != nil {
			args = append(args, "-rc-lookahead", fmt.Sprintf("%d", *finalParams.Lookahead))
		}
		if finalParams.TemporalAQ != nil {
			args = append(args, "-temporal-aq", BtoI(*finalParams.TemporalAQ))
		}
		if finalParams.Bframes != nil {
			args = append(args, "-bf", fmt.Sprintf("%d", *finalParams.Bframes))
		}
		if finalParams.Refs != nil {
			args = append(args, "-refs", fmt.Sprintf("%d", *finalParams.Refs))
		}

	} else if encoderName == "libx264" || encoderName == "libx265" {
		var codecParams []string
		if finalParams.Bframes != nil {
			codecParams = append(codecParams, fmt.Sprintf("bframes=%d", *finalParams.Bframes))
		}
		if finalParams.Refs != nil {
			codecParams = append(codecParams, fmt.Sprintf("refs=%d", *finalParams.Refs))
		}

		if len(codecParams) > 0 {
			paramString := strings.Join(codecParams, ":")
			if encoderName == "libx264" {
				args = append(args, "-x264-params", paramString)
			} else {
				args = append(args, "-x265-params", paramString)
			}
		}
	}

	return args
}
