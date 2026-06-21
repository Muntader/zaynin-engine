package analyzer

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	vodTypes "github.com/muntader/zaynin-engine/internal/vod/types"
	"github.com/muntader/zaynin-engine/pkg/encoder/types"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// findBestMatchForRule   best stream matching language (+ optional channel count).
func findBestMatchForRule(rule vodTypes.AudioTrack, streams []types.FfprobeStream) *types.FfprobeStream {
	var candidates []types.FfprobeStream

	for _, stream := range streams {
		langCode := "und"
		if lc, ok := stream.Tags["language"]; ok && lc != "" {
			langCode = lc
		}

		if langCode != rule.Select.Language {
			continue
		}

		if rule.Select.Channels > 0 && stream.Channels != rule.Select.Channels {
			continue
		}

		candidates = append(candidates, stream)
	}

	// pick highest quality among candidates
	return selectBestAudioStream(candidates)
}

// guess bitrate when ffprobe doesn't give us one
func getSourceBitrate(stream types.FfprobeStream) int {
	if stream.BitRate != "" {
		return parseBitrateString(stream.BitRate)
	}
	switch strings.ToLower(stream.CodecName) {
	case "dts", "dts-hd":
		return estimateDTSBitrate(stream)
	case "truehd", "mlp":
		return 4608000
	case "eac3", "eac-3":
		return estimateEAC3Bitrate(stream)
	case "ac3", "ac-3":
		return estimateAC3Bitrate(stream)
	case "aac":
		return estimateAACBitrate(stream)
	case "mp3":
		return 320000
	case "flac":
		return estimateFLACBitrate(stream)
	case "pcm_s16le", "pcm_s24le", "pcm_s32le":
		return estimatePCMBitrate(stream)
	case "opus":
		return estimateOpusBitrate(stream)
	case "vorbis":
		return estimateVorbisBitrate(stream)
	default:
		log.Printf("Unknown codec '%s', using default bitrate estimation", stream.CodecName)
		return getDefaultBitrateForChannels(stream.Channels)
	}
}

func getOptimalStereoBitrate(sourceBitrate int) string {
	qualityTiers := []struct {
		threshold int
		bitrate   string
	}{
		{192000, "192k"}, {160000, "160k"}, {128000, "128k"}, {96000, "96k"}, {64000, "64k"},
	}
	for _, tier := range qualityTiers {
		if sourceBitrate >= tier.threshold {
			return tier.bitrate
		}
	}
	return "64k"
}

func getOptimalMonoBitrate(sourceBitrate int) string {
	qualityTiers := []struct {
		threshold int
		bitrate   string
	}{
		{128000, "128k"}, {96000, "96k"}, {64000, "64k"}, {48000, "48k"},
	}
	for _, tier := range qualityTiers {
		if sourceBitrate >= tier.threshold {
			return tier.bitrate
		}
	}
	return "48k"
}

func estimateDTSBitrate(stream types.FfprobeStream) int {
	if stream.Channels >= 6 {
		return 1536000
	}
	return 768000
}

func estimateEAC3Bitrate(stream types.FfprobeStream) int {
	if stream.Channels >= 6 {
		return 768000
	}
	return 192000
}

func estimateAC3Bitrate(stream types.FfprobeStream) int {
	if stream.Channels >= 6 {
		return 448000
	}
	return 192000
}

func estimateAACBitrate(stream types.FfprobeStream) int {
	if stream.Channels >= 6 {
		return 384000
	} else if stream.Channels >= 2 {
		return 192000
	}
	return 128000
}

func estimateFLACBitrate(stream types.FfprobeStream) int {
	sampleRate := 48000
	if sr, err := strconv.Atoi(stream.SampleRate); err == nil {
		sampleRate = sr
	}
	pcmBitrate := sampleRate * 16 * stream.Channels
	return int(float64(pcmBitrate) * 0.6)
}

func estimatePCMBitrate(stream types.FfprobeStream) int {
	sampleRate := 48000
	if sr, err := strconv.Atoi(stream.SampleRate); err == nil {
		sampleRate = sr
	}
	bitDepth := 16
	if strings.Contains(stream.CodecName, "24") {
		bitDepth = 24
	} else if strings.Contains(stream.CodecName, "32") {
		bitDepth = 32
	}
	return sampleRate * bitDepth * stream.Channels
}

func estimateOpusBitrate(stream types.FfprobeStream) int {
	if stream.Channels >= 6 {
		return 256000
	} else if stream.Channels >= 2 {
		return 128000
	}
	return 64000
}

func estimateVorbisBitrate(stream types.FfprobeStream) int {
	if stream.Channels >= 6 {
		return 256000
	} else if stream.Channels >= 2 {
		return 192000
	}
	return 128000
}

func getDefaultBitrateForChannels(channels int) int {
	switch {
	case channels >= 8:
		return 768000
	case channels >= 6:
		return 384000
	case channels >= 2:
		return 192000
	default:
		return 128000
	}
}

func parseBitrateString(bitrateStr string) int {
	if bitrateStr == "" {
		return 0
	}
	cleanedStr := strings.TrimSpace(strings.ToLower(bitrateStr))
	var multiplier float64 = 1.0
	numericPart := cleanedStr

	if strings.HasSuffix(cleanedStr, "kbps") || strings.HasSuffix(cleanedStr, "k") {
		multiplier = 1000
		numericPart = strings.TrimRight(strings.TrimSuffix(cleanedStr, "bps"), "k")
	} else if strings.HasSuffix(cleanedStr, "mbps") || strings.HasSuffix(cleanedStr, "m") {
		multiplier = 1000000
		numericPart = strings.TrimRight(strings.TrimSuffix(cleanedStr, "bps"), "m")
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(numericPart), 64)
	if err != nil {
		log.Printf("Warning: could not parse bitrate '%s': %v", bitrateStr, err)
		return 0
	}
	return int(val * multiplier)
}

func formatChannels(channels int) string {
	switch channels {
	case 1:
		return "Mono"
	case 2:
		return "Stereo"
	case 6:
		return "5.1 Surround"
	case 8:
		return "7.1 Surround"
	default:
		return fmt.Sprintf("%dch", channels)
	}
}

func getSourceBitrateKbps(ffprobeData *types.FfprobeOutput) (int, error) {
	if ffprobeData.Format.BitRate != "" {
		if bitrateBps, err := strconv.ParseFloat(ffprobeData.Format.BitRate, 64); err == nil && bitrateBps > 0 {
			log.Printf("Bitrate found from container format: %.0f bps", bitrateBps)
			return int(bitrateBps / 1000), nil
		}
	}

	for _, stream := range ffprobeData.Streams {
		if stream.CodecType == "video" && stream.BitRate != "" {
			if bitrateBps, err := strconv.ParseFloat(stream.BitRate, 64); err == nil && bitrateBps > 0 {
				log.Printf("Bitrate found from video stream #%d: %.0f bps", stream.Index, bitrateBps)
				return int(bitrateBps / 1000), nil
			}
			break
		}
	}

	if ffprobeData.Format.Size != "" && ffprobeData.Format.Duration != "" {
		fileSizeBytes, errSize := strconv.ParseFloat(ffprobeData.Format.Size, 64)
		durationSec, errDur := strconv.ParseFloat(ffprobeData.Format.Duration, 64)

		if errSize == nil && errDur == nil && durationSec > 0 {
			bitrateBps := (fileSizeBytes * 8) / durationSec
			log.Printf("Bitrate calculated from file size (%.0f bytes) and duration (%.2f s): %.0f bps", fileSizeBytes, durationSec, bitrateBps)
			return int(bitrateBps / 1000), nil
		}
	}

	return 0, fmt.Errorf("could not determine bitrate from format, stream, or calculation")
}

func generateTaskOutputPath(
	baseOutputDir string,
	baseName string,
	streamSubDir string,
	renditionLabel string,
	langCode string,
	streamIndex int,
	extension string,
) string {
	targetDir := filepath.Join(baseOutputDir, streamSubDir)

	safeLang := "und"
	if langCode != "" {
		safeLang = strings.Split(langCode, "-")[0]
	}

	var finalFilename string
	switch streamSubDir {
	case "video":
		finalFilename = fmt.Sprintf("%s_%s.%s", baseName, renditionLabel, extension)
	case "audio":
		safeLabel := strings.NewReplacer(" ", "_", "(", "", ")", "").Replace(renditionLabel)
		finalFilename = fmt.Sprintf("audio_%s_%s_%d.%s", safeLang, safeLabel, streamIndex, extension)
	case "subtitle":
		finalFilename = fmt.Sprintf("sub_%s_%d.%s", safeLang, streamIndex, extension)
	default:
		finalFilename = fmt.Sprintf("%s_%s_%d.%s", streamSubDir, safeLang, streamIndex, extension)
	}

	return filepath.Join(targetDir, finalFilename)
}

func getProfileFromHardware(hw string) EncodingProfile {
	hw = strings.ToLower(hw)

	// nvidia
	if strings.Contains(hw, "nvenc") ||
		strings.Contains(hw, "h264_nvenc") ||
		strings.Contains(hw, "hevc_nvenc") ||
		strings.Contains(hw, "nvidia") ||
		strings.Contains(hw, "cuda") {
		return ProfileNvidiaDefault
	}

	// intel
	if strings.Contains(hw, "qsv") ||
		strings.Contains(hw, "h264_qsv") ||
		strings.Contains(hw, "hevc_qsv") ||
		strings.Contains(hw, "intel") ||
		strings.Contains(hw, "vaapi") {
		return ProfileIntelQSVDefault
	}

	// amd
	if strings.Contains(hw, "amf") ||
		strings.Contains(hw, "h264_amf") ||
		strings.Contains(hw, "hevc_amf") ||
		strings.Contains(hw, "amd") ||
		strings.Contains(hw, "radeon") {
		return ProfileAMDDefault
	}

	// apple
	if strings.Contains(hw, "videotoolbox") ||
		strings.Contains(hw, "h264_videotoolbox") ||
		strings.Contains(hw, "hevc_videotoolbox") ||
		strings.Contains(hw, "apple") ||
		strings.Contains(hw, "m1") ||
		strings.Contains(hw, "m2") ||
		strings.Contains(hw, "m3") {
		return ProfileAppleDefault
	}

	return ProfileCPUDefault
}

func getHWAccelFromEncoder(encoder string) string {
	if strings.Contains(encoder, "nvenc") {
		return "cuda"
	}
	if strings.Contains(encoder, "qsv") {
		return "qsv"
	}
	return ""
}

func getCodecSettings(profile EncodingProfile, outputCodec string, isSourceHDR bool) (codec, hwAccel, pixFmt, codecProfile string) {
	pixFmt, codecProfile = "yuv420p", "high"

	if outputCodec == "hevc" {
		codecProfile = "main"
		pixFmt = "yuv420p10le"
		if isSourceHDR {
			codecProfile = "main10"
			pixFmt = "p010le"
		}
	}

	switch profile {
	case ProfileNvidiaDefault:
		hwAccel = "cuda"
		if outputCodec == "hevc" {
			codec = "hevc_nvenc"
		} else {
			codec = "h264_nvenc"
			pixFmt = "yuv420p"
			codecProfile = "high"
		}
	case ProfileIntelQSVDefault:
		hwAccel = "qsv"
		if outputCodec == "hevc" {
			codec = "hevc_qsv"
		} else {
			codec = "h264_qsv"
			pixFmt = "yuv420p"
			codecProfile = "high"
		}
	default:
		hwAccel = ""
		if outputCodec == "hevc" {
			codec = "libx265"
		} else {
			codec = "libx264"
			pixFmt = "yuv420p"
			codecProfile = "high"
		}
	}
	return codec, hwAccel, pixFmt, codecProfile
}

func getCodecLevel(width, height int, outputCodec string, framerate float64) string {
	pixels := width * height
	isHEVC := strings.Contains(outputCodec, "hevc")
	mps := float64(pixels) * framerate

	if isHEVC {
		if mps <= 552960000 {
			return "5.1"
		}
		if mps <= 221184000 {
			return "5"
		}
		if mps <= 110496000 {
			return "4.1"
		}
		return "2.1"
	}

	if mps <= 245760000 {
		return "5.2"
	}
	if mps <= 111800000 {
		return "5.1"
	}
	if mps <= 65536000 {
		return "4.2"
	}
	return "3"
}

func extractTitle(tags map[string]string) string {
	if title, ok := tags["title"]; ok && title != "" {
		return title
	}
	return ""
}

func detectSubtitleType(stream types.FfprobeStream, title string) string {
	titleLower := strings.ToLower(title)
	if isStreamFlagSet(stream, "forced") || strings.Contains(titleLower, "forced") {
		return "forced"
	}
	if strings.Contains(titleLower, "sdh") || strings.Contains(titleLower, "hearing") {
		return "sdh"
	}
	if strings.Contains(titleLower, "commentary") || strings.Contains(titleLower, "comment") {
		return "commentary"
	}
	return "regular"
}

func isImageBasedSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "dvd_subtitle", "pgs", "hdmv_pgs_subtitle", "dvb_subtitle":
		return true
	default:
		return false
	}
}

func normalizeAndFormatLanguage(rawLang string) (code string, label string) {
	if rawLang == "" || strings.ToLower(strings.TrimSpace(rawLang)) == "und" {
		return "und", "Unknown"
	}

	cleanLang := strings.ToLower(strings.TrimSpace(rawLang))

	tag, err := language.Parse(cleanLang)
	if err != nil {
		log.Printf("Error parsing language tag '%s': %v", cleanLang, err)
		return cleanLang, strings.Title(cleanLang)
	}

	code = tag.String()
	baseTag, _ := tag.Base()
	englishLanguages := display.English.Languages()
	languageName := englishLanguages.Name(baseTag)

	if languageName == "" {
		languageName = strings.Title(baseTag.String())
	}

	return code, languageName
}

func getLanguageName(stream *types.FfprobeStream, langCode string) string {
	if title, ok := stream.Tags["title"]; ok && title != "" {
		return title
	}
	_, label := normalizeAndFormatLanguage(langCode)
	return label
}

// selectBestAudioStream   more channels wins, then higher bitrate.
func selectBestAudioStream(streams []types.FfprobeStream) *types.FfprobeStream {
	if len(streams) == 0 {
		return nil
	}

	sort.Slice(streams, func(i, j int) bool {
		if streams[i].Channels != streams[j].Channels {
			return streams[i].Channels > streams[j].Channels
		}
		bitrateI := getSourceBitrate(streams[i])
		bitrateJ := getSourceBitrate(streams[j])
		return bitrateI > bitrateJ
	})

	return &streams[0]
}

// codecs we can mux in HLS/DASH without re-encoding
func isHLSCompatibleCodec(codecName string) bool {
	compatible := []string{"aac", "eac3", "eac-3", "ac3", "ac-3"}
	codec := strings.ToLower(codecName)
	for _, c := range compatible {
		if codec == c {
			return true
		}
	}
	return false
}

// ffprobe disposition is 1/0 ints in JSON
func isStreamFlagSet(stream types.FfprobeStream, flagName string) bool {
	switch strings.ToLower(flagName) {
	case "default":
		return stream.Disposition["Default"] == 1
	case "forced":
		return stream.Disposition["Forced"] == 1
	case "hearing_impaired":
		return stream.Disposition["HearingImpaired"] == 1
	case "visual_impaired":
		return stream.Disposition["VisualImpaired"] == 1
	case "comment":
		return stream.Disposition["Comment"] == 1
	case "dub":
		return stream.Disposition["Dub"] == 1
	case "original":
		return stream.Disposition["Original"] == 1
	default:
		return false
	}
}
