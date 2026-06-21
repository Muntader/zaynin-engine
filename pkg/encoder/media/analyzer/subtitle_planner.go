package analyzer

import (
	"log"
	"sort"
	"strings"

	vodTypes "github.com/muntader/zaynin-engine/internal/vod/types"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/subtitle"

	"github.com/muntader/zaynin-engine/pkg/encoder/types"
)

// subtitle codecs we know how to turn into webvtt (text + image/OCR)
var convertibleSubtitleCodecs = map[string]struct{}{
	"subrip":   {},
	"srt":      {},
	"ass":      {},
	"ssa":      {},
	"webvtt":   {},
	"mov_text": {},

	"dvd_subtitle":      {},
	"hdmv_pgs_subtitle": {},
	"pgs":               {},
	"dvb_subtitle":      {},
}

func isConvertibleSubtitle(codec string) bool {
	_, ok := convertibleSubtitleCodecs[strings.ToLower(codec)]
	return ok
}

func planSubtitleTasks(
	allSourceStreams []types.FfprobeStream,
	config *vodTypes.Subtitles,
	sourcePath string,
	outputDir string,
	baseName string,
) []subtitle.Task {
	switch config.Mode {
	case "none":
		log.Println("Subtitle mode is 'none'. Skipping all subtitle processing.")
		return []subtitle.Task{}
	case "custom":
		log.Println("Subtitle mode is 'custom'. Processing tracks specified in config.")
		return planCustomSubtitles(allSourceStreams, config, sourcePath, outputDir, baseName)
	default:
		log.Println("Subtitle mode is 'auto'. Automatically selecting and converting best subtitle tracks.")
		return planAutomaticSubtitles(allSourceStreams, sourcePath, outputDir, baseName)
	}
}

func planCustomSubtitles(
	allSourceStreams []types.FfprobeStream,
	config *vodTypes.Subtitles,
	sourcePath string,
	outputDir string,
	baseName string,
) []subtitle.Task {
	var finalTasks []subtitle.Task
	processedIndices := make(map[int]bool)

	for _, desiredTrack := range config.Tracks {
		foundMatch := false
		for _, sourceStream := range allSourceStreams {
			if sourceStream.CodecType != "subtitle" || processedIndices[sourceStream.Index] {
				continue
			}

			langCode, _ := normalizeAndFormatLanguage(sourceStream.Tags["language"])
			isForced := isStreamFlagSet(sourceStream, "forced")

			if desiredTrack.Select.Language == langCode && desiredTrack.Select.Forced == isForced && isConvertibleSubtitle(sourceStream.CodecName) {
				log.Printf("Found match for custom subtitle track (lang: %s, forced: %v) -> source stream #%d",
					desiredTrack.Select.Language, desiredTrack.Select.Forced, sourceStream.Index)
				task := subtitle.Task{
					InputFile:    sourcePath,
					OutputFile:   generateTaskOutputPath(outputDir, baseName, "subtitle", desiredTrack.Label, langCode, sourceStream.Index, "vtt"),
					SourceIndex:  sourceStream.Index,
					Language:     langCode,
					Label:        desiredTrack.Label,
					IsImageBased: isImageBasedSubtitle(sourceStream.CodecName),
					Action:       desiredTrack.Action,
				}
				finalTasks = append(finalTasks, task)
				processedIndices[sourceStream.Index] = true
				foundMatch = true
				break
			}
		}
		if !foundMatch {
			log.Printf("Warning: No matching source stream found for custom subtitle track (lang: %s, forced: %v)",
				desiredTrack.Select.Language, desiredTrack.Select.Forced)
		}
	}
	return finalTasks
}

type subtitleAnalysisTemp struct {
	subtitle.Task
	SubtitleType string
	IsDefault    bool
}

func planAutomaticSubtitles(
	allSourceStreams []types.FfprobeStream,
	sourcePath string,
	outputDir string,
	baseName string,
) []subtitle.Task {
	var analyses []subtitleAnalysisTemp
	for _, stream := range allSourceStreams {
		if stream.CodecType == "subtitle" && isConvertibleSubtitle(stream.CodecName) {
			analyses = append(analyses, analyzeSingleSubtitleStream(stream, sourcePath, outputDir, baseName))
		}
	}
	optimizedAnalyses := optimizeSubtitleSelection(analyses)
	var finalTasks []subtitle.Task
	for _, analysis := range optimizedAnalyses {
		finalTasks = append(finalTasks, analysis.Task)
	}
	return finalTasks
}

func analyzeSingleSubtitleStream(stream types.FfprobeStream, sourcePath, outputDir, baseName string) subtitleAnalysisTemp {
	langCode, langLabel := normalizeAndFormatLanguage(stream.Tags["language"])
	title := extractTitle(stream.Tags)
	if title != "" {
		langLabel = title
	}

	return subtitleAnalysisTemp{
		Task: subtitle.Task{
			InputFile:    sourcePath,
			OutputFile:   generateTaskOutputPath(outputDir, baseName, "subtitle", "", langCode, stream.Index, "vtt"),
			SourceIndex:  stream.Index,
			Language:     langCode,
			Label:        langLabel,
			IsImageBased: isImageBasedSubtitle(stream.CodecName),
			Action:       "convert_to_vtt",
		},
		SubtitleType: detectSubtitleType(stream, title),
		IsDefault:    isStreamFlagSet(stream, "default"),
	}
}

// one track per language   prefer default flag, then forced/full over sdh
func optimizeSubtitleSelection(streams []subtitleAnalysisTemp) []subtitleAnalysisTemp {
	if len(streams) < 2 {
		return streams
	}
	languageGroups := make(map[string][]subtitleAnalysisTemp)
	for _, s := range streams {
		languageGroups[s.Language] = append(languageGroups[s.Language], s)
	}

	var optimized []subtitleAnalysisTemp
	for _, group := range languageGroups {
		sort.Slice(group, func(i, j int) bool {
			if group[i].IsDefault != group[j].IsDefault {
				return group[i].IsDefault
			}
			return getSubtitleTypeScore(group[i].SubtitleType) > getSubtitleTypeScore(group[j].SubtitleType)
		})
		if len(group) > 0 {
			optimized = append(optimized, group[0])
		}
	}
	return optimized
}

func getSubtitleTypeScore(subType string) int {
	switch subType {
	case "forced":
		return 5
	case "full":
		return 4
	case "regular":
		return 3
	case "sdh":
		return 2
	case "cc":
		return 1
	default:
		return 0
	}
}
