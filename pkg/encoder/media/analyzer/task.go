package analyzer

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	vodTypes "github.com/muntader/zaynin-engine/internal/vod/types"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/audio"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/video"
	"github.com/muntader/zaynin-engine/pkg/encoder/types"

	"github.com/muntader/zaynin-engine/pkg/encoder/ffmpeg"
	_ "gitlab.com/gjuyn/go-iso/iso"
)

// Run   ffprobe the source and plan audio/video/subtitle tasks.
func Run(path, outputDir, baseName string, config *types.AnalysisConfig) (*types.AnalysisReport, error) {
	log.Println("--- Starting Job Planner ---")

	ffprobeData, err := ffmpeg.GetMediaInfo(context.Background(), path)
	if err != nil {
		return nil, fmt.Errorf("could not get media info: %w", err)
	}

	report := &types.AnalysisReport{Source: ffprobeData.Format}

	sourceBitrate, err := getSourceBitrateKbps(ffprobeData)
	if err != nil {
		log.Printf("Warning: %v. The automatic ladder will be generated using presets.", err)
	}

	// video
	var videoStream *types.FfprobeStream
	for i, stream := range ffprobeData.Streams {
		if stream.CodecType == "video" {
			videoStream = &ffprobeData.Streams[i]
			break
		}
	}
	if videoStream != nil {
		report.Video = planVideoTasks(videoStream, config, sourceBitrate, path, outputDir, baseName)
	}

	// audio
	report.Audio = planAudioTasks(ffprobeData.Streams, config.Outputs.StreamingPackage.Audio, path, outputDir, baseName)

	// subtitles
	report.Subtitles = planSubtitleTasks(ffprobeData.Streams, config.Outputs.StreamingPackage.Subtitles, path, outputDir, baseName)

	log.Printf("--- Job Planner Finished ---")
	return report, nil
}

// planAudioTasks   custom mode only uses config tracks; auto does overrides then fill-in.
func planAudioTasks(
	allStreams []types.FfprobeStream,
	audioConfig *vodTypes.Audio,
	sourcePath, outputDir, baseName string,
) []audio.Task {
	log.Println("Planning audio tasks...")

	var audioStreams []types.FfprobeStream
	for _, s := range allStreams {
		if s.CodecType == "audio" {
			audioStreams = append(audioStreams, s)
		}
	}

	if len(audioStreams) == 0 {
		log.Println("No audio streams found in the source file.")
		return nil
	}

	if audioConfig == nil {
		audioConfig = &vodTypes.Audio{Mode: "auto"}
	}
	log.Printf("Audio mode set to: '%s'", audioConfig.Mode)

	var allAudioTasks []audio.Task
	var hasUserDefault = false

	// custom: only the tracks listed in config, ignore everything else
	if audioConfig.Mode == "custom" {
		for i, rule := range audioConfig.Tracks {
			log.Printf("Processing custom rule #%d for language '%s'", i+1, rule.Select.Language)
			bestMatch := findBestMatchForRule(rule, audioStreams)
			if bestMatch == nil {
				log.Printf("Warning: No matching stream found for rule with language '%s' and channels %d", rule.Select.Language, rule.Select.Channels)
				continue
			}

			log.Printf("Found match for custom rule: Index %d", bestMatch.Index)

			task := audio.Task{
				Language:       rule.Select.Language,
				Label:          rule.Output.Label,
				InputFile:      sourcePath,
				OutputFile:     generateTaskOutputPath(outputDir, baseName, "audio", "custom", rule.Select.Language, bestMatch.Index, "m4a"),
				Codec:          rule.Output.Codec,
				Bitrate:        rule.Output.Bitrate,
				Channels:       bestMatch.Channels,
				SourceChannels: bestMatch.Channels,
				SampleRate:     "48000",
				Description:    rule.Output.Label,
				IsDefault:      rule.Output.IsDefault,
				SourceIndex:    bestMatch.Index,
			}

			if rule.Output.Channels > 0 {
				task.Channels = rule.Output.Channels
			}
			if rule.Output.IsDefault {
				hasUserDefault = true
			}
			allAudioTasks = append(allAudioTasks, task)
		}
	}

	// auto: overrides first, then best stream per language we haven't touched
	if audioConfig.Mode == "auto" {
		languageGroups := make(map[string][]types.FfprobeStream)
		for _, stream := range audioStreams {
			langCode := "und"
			if lc, ok := stream.Tags["language"]; ok && lc != "" {
				langCode = lc
			}
			languageGroups[langCode] = append(languageGroups[langCode], stream)
		}

		handledLanguages := make(map[string]bool)

		// pass 1   user overrides from Tracks
		for _, rule := range audioConfig.Tracks {
			bestMatch := findBestMatchForRule(rule, audioStreams)
			if bestMatch == nil {
				log.Printf("Warning: No matching stream found for override rule with language '%s'", rule.Select.Language)
				continue
			}
			log.Printf("Applying user override for language '%s' (Source Index: %d)", rule.Select.Language, bestMatch.Index)

			task := audio.Task{
				Language:       rule.Select.Language,
				Label:          rule.Output.Label,
				InputFile:      sourcePath,
				OutputFile:     generateTaskOutputPath(outputDir, baseName, "audio", "override", rule.Select.Language, bestMatch.Index, "m4a"),
				Codec:          rule.Output.Codec,
				Bitrate:        rule.Output.Bitrate,
				Channels:       bestMatch.Channels,
				SourceChannels: bestMatch.Channels,
				SampleRate:     "48000",
				Description:    rule.Output.Label,
				IsDefault:      rule.Output.IsDefault,
				SourceIndex:    bestMatch.Index,
			}

			if rule.Output.Channels > 0 {
				task.Channels = rule.Output.Channels
			}
			if rule.Output.IsDefault {
				hasUserDefault = true
			}
			allAudioTasks = append(allAudioTasks, task)
			handledLanguages[rule.Select.Language] = true
		}

		// pass 2   auto-pick best stream per language (skip ones we already handled)
		for langCode, streamsInGroup := range languageGroups {
			if handledLanguages[langCode] {
				log.Printf("Skipping automatic processing for language '%s' as it was handled by a user rule.", langCode)
				continue
			}

			bestStream := selectBestAudioStream(streamsInGroup)
			langName := getLanguageName(bestStream, langCode)

			log.Printf("Automatically processing best stream for language '%s' (%s): Index %d (%s, %s)",
				langCode, langName, bestStream.Index, bestStream.CodecName, formatChannels(bestStream.Channels))

			tasksForLang := createAdaptiveAudioTracks(
				*bestStream, sourcePath, outputDir, baseName, langCode, langName,
			)
			allAudioTasks = append(allAudioTasks, tasksForLang...)
		}
	}

	// make sure exactly one default track (unless user set explicit defaults)
	if len(allAudioTasks) > 0 {
		if hasUserDefault {
			for i := range allAudioTasks {
				isExplicitlyDefault := false
				for _, rule := range audioConfig.Tracks {
					if rule.Output.IsDefault && allAudioTasks[i].Language == rule.Select.Language && allAudioTasks[i].Label == rule.Output.Label {
						isExplicitlyDefault = true
						break
					}
				}
				if !isExplicitlyDefault {
					allAudioTasks[i].IsDefault = false
				}
			}
		} else {
			defaultFound := false
			for i := range allAudioTasks {
				if allAudioTasks[i].IsDefault {
					if defaultFound {
						allAudioTasks[i].IsDefault = false
					} else {
						defaultFound = true
					}
				}
			}
			if !defaultFound && len(allAudioTasks) > 0 {
				allAudioTasks[0].IsDefault = true
			}
		}
	}

	log.Printf("Planned a total of %d audio tasks.", len(allAudioTasks))
	return allAudioTasks
}

// planVideoTasks   renditions array, auto ladder, or mix of height-only + custom.
func planVideoTasks(
	stream *types.FfprobeStream,
	config *types.AnalysisConfig,
	sourceBitrate int,
	sourcePath, outputDir, baseName string,
) types.VideoAnalysis {
	// no config → just source metadata, no renditions
	if config == nil || config.Outputs == nil {
		return types.VideoAnalysis{
			SourceIndex: stream.Index,
			CodecName:   stream.CodecName,
			Width:       stream.Width,
			Height:      stream.Height,
			FrameRate:   parseFPS(stream.AvgFrameRate),
			IsHDR:       isHDR(*stream),
		}
	}

	pkgConfig := &config.Outputs.StreamingPackage
	videoConfig := (*pkgConfig).Video

	sourceInfo := &types.VideoAnalysis{
		SourceIndex: stream.Index, CodecName: stream.CodecName,
		Width: stream.Width, Height: stream.Height, FrameRate: parseFPS(stream.AvgFrameRate),
		IsHDR: isHDR(*stream),
	}

	var lowLevelTasks []types.EncodingTask

	if videoConfig.Renditions != nil {
		log.Println("Processing video based on 'renditions' array in config.")

		if len(videoConfig.Renditions) == 0 {
			log.Println("Renditions array is empty, generating fully automatic ABR ladder.")
			profile := getProfileFromHardware(config.JobSettings.HardwareAcceleration)
			outputCodec := videoConfig.Encoder
			fmt.Println(outputCodec)
			lowLevelTasks = GenerateAutomaticLadder(sourceInfo, sourceBitrate, profile, outputCodec, (*pkgConfig).Packaging, sourcePath, outputDir, baseName, config.Outputs.StreamingPackage.Video.Threads)
		} else {
			var heightOnlyRequests []int
			var customRequests []vodTypes.VideoRendition

			for _, rendition := range videoConfig.Renditions {
				// custom if any encoding knob is set; height-only otherwise
				isCustom := rendition.Encoder != "" ||
					rendition.Profile != "" ||
					rendition.Preset != "" ||
					//rendition.Tune != "" ||
					rendition.Level != "" ||
					rendition.RateControl.Bitrate != "" ||
					rendition.RateControl.MaxBitrate != "" ||
					rendition.RateControl.CRFValue > 0 ||
					rendition.RateControl.QPValue > 0 ||
					rendition.Filtering.HdrToSdr.Enable

				if rendition.Height > 0 && !isCustom {
					heightOnlyRequests = append(heightOnlyRequests, rendition.Height)
				} else {
					customRequests = append(customRequests, rendition)
				}
			}

			// height-only → auto generator
			if len(heightOnlyRequests) > 0 {
				log.Printf("Found %d height-only rendition requests. Generating automatic tasks for heights: %v", len(heightOnlyRequests), heightOnlyRequests)

				profile := getProfileFromHardware(config.JobSettings.HardwareAcceleration)
				outputCodec := videoConfig.Encoder
				if outputCodec == "" {
					outputCodec = "libx264"
				}
				referenceBitrate := getReferenceBitrate(sourceBitrate, sourceInfo)

				for _, height := range heightOnlyRequests {
					if height > sourceInfo.Height && (*pkgConfig).AllowUpscale {
						log.Printf("Skipping automatic rendition for height %dp: Upscaling is disabled.", height)
						continue
					}
					task := generateAutomaticTask(height, sourceInfo, referenceBitrate, profile, outputCodec, (*pkgConfig).Packaging, sourcePath, outputDir, baseName, config.Outputs.StreamingPackage.Video.Threads)
					lowLevelTasks = append(lowLevelTasks, task)
				}
			}

			// fully custom renditions
			if len(customRequests) > 0 {
				log.Printf("Found %d fully custom renditions. Translating them to tasks.", len(customRequests))
				for _, customRendition := range customRequests {
					if customRendition.Height > sourceInfo.Height && !(*pkgConfig).AllowUpscale {
						log.Printf("Skipping custom rendition for height %dp: Upscaling is disabled.", customRendition.Height)
						continue
					}
					task := translateCustomRenditionToTask(customRendition, sourceInfo, (*pkgConfig).Packaging, sourcePath, outputDir, baseName, config.Outputs.StreamingPackage.Video.Threads)
					lowLevelTasks = append(lowLevelTasks, task)
				}
			}
		}
	}

	sourceInfo.Renditions = lowLevelTasks
	return *sourceInfo
}

// translateCustomRenditionToTask   one config rendition → one EncodingTask.
func translateCustomRenditionToTask(rendition vodTypes.VideoRendition, sourceInfo *types.VideoAnalysis, pkgConfig vodTypes.Packaging, sourcePath, outputDir, baseName string, threads int) types.EncodingTask {
	log.Printf("Translating custom rendition for '%s' (%dp)", rendition.Tag, rendition.Height)

	// dimensions
	sourceAspectRatio := float64(sourceInfo.Width) / float64(sourceInfo.Height)
	taskWidth := int(math.Round(float64(rendition.Height)*sourceAspectRatio/2.0)) * 2

	// encoder + rate control
	finalCodec := rendition.Encoder

	fmt.Println("get codec by the profile", finalCodec)
	profile := getProfileFromHardware(finalCodec)
	finalProfile := rendition.Profile
	finalTune := rendition.Tune

	finalPreset := rendition.Preset
	if finalPreset == "" {
		finalPreset = getPresetForRendition(rendition.Height, profile)
	}

	finalLevel := rendition.Level
	if finalLevel != "" {
		log.Printf("Warning: Manually setting '-level %s' for rendition %dp. Consider letting FFmpeg auto-determine for best compatibility.", finalLevel, rendition.Height)
	}

	finalRateControl := video.RateControl{
		VBR: &video.BitrateConfig{TargetBitrate: rendition.RateControl.Bitrate, MaxBitrate: rendition.RateControl.MaxBitrate, BufferSize: rendition.RateControl.BufferSize},
		CRF: rendition.RateControl.CRFValue,
		QP:  rendition.RateControl.QPValue,
	}

	// GOP from segment duration
	gopSize := int(math.Round(sourceInfo.FrameRate * float64(pkgConfig.SegmentDurationSeconds)))
	if gopSize == 0 {
		gopSize = int(math.Round(sourceInfo.FrameRate * 2.0)) // segment duration 0 → ~2s fallback
	}

	var hdrConfig *video.HDRConfig
	pixelFormat := "yuv420p"

	if sourceInfo.IsHDR {
		if rendition.Filtering.HdrToSdr.Enable {
			algo := rendition.Filtering.HdrToSdr.TonemapAlgorithm
			if algo == "" {
				algo = "hable"
			}
			hdrConfig = &video.HDRConfig{ToneMap: algo}
			pixelFormat = "yuv420p"
			log.Printf("Rendition '%s': Applying HDR->SDR tonemapping with '%s' algorithm.", rendition.Tag, algo)
		} else {
			hdrConfig = &video.HDRConfig{Passthrough: true}
			pixelFormat = "p010le"
			log.Printf("Rendition '%s': Preserving HDR signal.", rendition.Tag)
		}
	}

	// x264/x265 extra params for CBR-ish packaging
	var codecExtraArgsList []string
	if finalCodec == "libx264" {
		codecExtraArgsList = append(codecExtraArgsList, "scenecut=0")
		codecExtraArgsList = append(codecExtraArgsList, "nal-hrd=cbr")
		codecExtraArgsList = append(codecExtraArgsList, "force-cfr=1")
	} else if finalCodec == "libx265" {
		codecExtraArgsList = append(codecExtraArgsList, "scenecut=0")
		codecExtraArgsList = append(codecExtraArgsList, "hrd-conformance=cbr")
	}
	codecExtraArgs := strings.Join(codecExtraArgsList, ":")

	task := types.EncodingTask{
		Name:        baseName,
		InputFile:   sourcePath,
		OutputFile:  generateTaskOutputPath(outputDir, baseName, "video", strconv.Itoa(rendition.Height), rendition.Tag, -1, "mp4"),
		HWAccel:     getHWAccelFromEncoder(finalCodec),
		Codec:       finalCodec,
		RateControl: finalRateControl,
		GOP:         video.GOP{FPS: fmt.Sprintf("%.3f", sourceInfo.FrameRate), GOPSize: gopSize, KeyintMin: gopSize, BFrames: getBFramesForProfile(profile)},
		Params: video.Params{
			Tune:           finalTune,
			Preset:         finalPreset,
			Profile:        finalProfile,
			Level:          finalLevel,
			PixelFormat:    pixelFormat,
			CodecExtraArgs: codecExtraArgs,
		},
		VideoFilter: getVideoFilter(profile, taskWidth, rendition.Height, hdrConfig, sourceInfo.IsHDR),
		Width:       taskWidth,
		Height:      rendition.Height,
		HDR:         hdrConfig,
		Threads:     threads,
	}

	return task
}
