package analyzer

import (
	"fmt"
	"log"

	"github.com/muntader/zaynin-engine/pkg/encoder/media/audio"
	"github.com/muntader/zaynin-engine/pkg/encoder/types"
)

type AudioProfile struct {
	Name        string
	Bitrate     string
	Channels    int
	SampleRate  string
	Description string
	Priority    int // lower = higher priority
}

// At most 2 tracks per source   stereo downmix + maybe original surround.
func GenerateAdaptiveAudioProfiles(sourceChannels int, sourceBitrate int, codecName string) []AudioProfile {
	var profiles []AudioProfile

	isSourceHLSCompatible := isHLSCompatibleCodec(codecName)

	switch {
	case sourceChannels >= 6:
		profiles = append(profiles, AudioProfile{
			Name:        "stereo_192k",
			Bitrate:     "192k",
			Channels:    2,
			SampleRate:  "48000",
			Description: "Stereo (High)",
			Priority:    1,
		})

		if isSourceHLSCompatible && sourceBitrate <= 640000 {
			profiles = append(profiles, AudioProfile{
				Name:        "surround_original",
				Bitrate:     fmt.Sprintf("%dk", sourceBitrate/1000),
				Channels:    sourceChannels,
				SampleRate:  "48000",
				Description: fmt.Sprintf("%s (Original)", formatChannels(sourceChannels)),
				Priority:    2,
			})
		}

	case sourceChannels == 2:
		targetBitrate := getOptimalStereoBitrate(sourceBitrate)
		profiles = append(profiles, AudioProfile{
			Name:        fmt.Sprintf("stereo_%s", targetBitrate),
			Bitrate:     targetBitrate,
			Channels:    2,
			SampleRate:  "48000",
			Description: "Stereo",
			Priority:    1,
		})

	default:
		var targetChannels int
		var targetBitrate string
		var desc string

		if sourceChannels == 1 {
			targetChannels = 1
			targetBitrate = getOptimalMonoBitrate(sourceBitrate)
			desc = "Mono"
		} else {
			targetChannels = 2
			targetBitrate = getOptimalStereoBitrate(sourceBitrate)
			desc = "Stereo"
		}

		profiles = append(profiles, AudioProfile{
			Name:        "primary",
			Bitrate:     targetBitrate,
			Channels:    targetChannels,
			SampleRate:  "48000",
			Description: desc,
			Priority:    1,
		})
	}

	log.Printf("Generated %d audio profile(s) for %dch source (%s)", len(profiles), sourceChannels, codecName)
	return profiles
}

func createAdaptiveAudioTracks(
	stream types.FfprobeStream,
	sourcePath, outputDir, baseName, langCode, langName string,
) []audio.Task {
	var tasks []audio.Task
	sourceBitrate := getSourceBitrate(stream)

	profiles := GenerateAdaptiveAudioProfiles(stream.Channels, sourceBitrate, stream.CodecName)

	for _, profile := range profiles {
		task := audio.Task{
			Language:       langCode,
			Label:          langName,
			InputFile:      sourcePath,
			OutputFile:     generateTaskOutputPath(outputDir, baseName, "audio", profile.Name, langCode, stream.Index, "m4a"),
			Codec:          "aac",
			Bitrate:        profile.Bitrate,
			SampleRate:     profile.SampleRate,
			Channels:       profile.Channels,
			SourceChannels: stream.Channels,
			Description:    profile.Description,
			IsDefault:      profile.Priority == 1,
			SourceIndex:    stream.Index,
		}
		tasks = append(tasks, task)
	}
	return tasks
}
