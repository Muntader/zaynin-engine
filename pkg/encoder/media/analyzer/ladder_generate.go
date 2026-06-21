package analyzer

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	vodTypes "github.com/muntader/zaynin-engine/internal/vod/types"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/video"

	"github.com/muntader/zaynin-engine/pkg/encoder/types"
)

// maps hw_accel string → encoder profile logic
type EncodingProfile string

const (
	ProfileCPUDefault      EncodingProfile = "cpu_default"
	ProfileNvidiaDefault   EncodingProfile = "nvidia_nvenc"
	ProfileIntelQSVDefault EncodingProfile = "intel_qsv"
	ProfileAMDDefault      EncodingProfile = "amd_vaapi"
	ProfileAppleDefault    EncodingProfile = "apple_av1"
)

// standard ABR heights, high to low
var MasterLadder = []int{4320, 2160, 1440, 1080, 720, 576, 480, 360, 240}

const (
	cpuMaxrateMultiplier    = 2.2
	cpuBufsizeMultiplier    = 1.5
	gpuMaxrateMultiplier    = 1.5
	gpuBufsizeMultiplier    = 2.0
	minRenditionBitrateKbps = 150
	bitrateScalingExponent  = 0.75
	fileSizeReductionFactor = 0.85
	h265MaxResolution       = 2160

	// GPU encoders choke on tiny widths (<256px)
	gpuMinRenditionWidth = 256
)

// relative efficiency vs h264 for bitrate estimates
var CodecEfficiencyFactors = map[string]float64{
	"av1": 2.0, "libaom-av1": 2.0, "hevc": 1.6, "libx265": 1.6,
	"vp9": 1.5, "libvpx-vp9": 1.5, "h264": 1.0, "libx264": 1.0,
}

// fallback kbps when we can't read source bitrate
var BitratePresets = map[int]int{
	4320: 35000, 2160: 16000, 1440: 9000, 1080: 5000, 720: 2800,
	576: 1800, 480: 1200, 360: 800, 240: 400,
}

// cap ladder for hevc   not everything does 8K hw decode
func getFilteredLadder(outputCodec string) []int {
	isH265 := strings.Contains(strings.ToLower(outputCodec), "265") || strings.Contains(strings.ToLower(outputCodec), "hevc")
	if isH265 {
		log.Printf("INFO: H.265 codec detected. Limiting ladder to max resolution of %dp for compatibility.", h265MaxResolution)
		var filteredLadder []int
		for _, height := range MasterLadder {
			if height <= h265MaxResolution {
				filteredLadder = append(filteredLadder, height)
			}
		}
		return filteredLadder
	}
	return MasterLadder
}

// GenerateAutomaticLadder   full ABR from source resolution down the master ladder.
func GenerateAutomaticLadder(
	sourceInfo *types.VideoAnalysis,
	sourceBitrate int,
	profile EncodingProfile,
	outputCodec string,
	packagingConfig vodTypes.Packaging,
	sourcePath string,
	outputDir string,
	baseName string,
	threads int,
) []types.EncodingTask {

	log.Println("--- Generating Automatic ABR Ladder ---")
	log.Println(profile)
	if sourceInfo.Height == 0 || sourceInfo.Width == 0 {
		return []types.EncodingTask{}
	}

	ladder := getFilteredLadder(outputCodec)
	if len(ladder) == 0 {
		return []types.EncodingTask{}
	}

	referenceBitrate := getReferenceBitrate(sourceBitrate, sourceInfo)
	sourceAspectRatio := float64(sourceInfo.Width) / float64(sourceInfo.Height)
	isGpuProfile := (profile == ProfileNvidiaDefault || profile == ProfileIntelQSVDefault)

	var tasks []types.EncodingTask
	addedHeights := make(map[int]bool)

	// top rung   cap to codec max if source is huge
	topHeight := sourceInfo.Height
	maxAllowedHeight := ladder[0]
	if topHeight > maxAllowedHeight {
		log.Printf("Source height (%dp) exceeds the maximum allowed for %s (%dp). Capping top rendition at %dp.",
			topHeight, outputCodec, maxAllowedHeight, maxAllowedHeight)
		topHeight = maxAllowedHeight
	}

	// always include top (maybe capped)
	topTask := generateAutomaticTask(topHeight, sourceInfo, referenceBitrate, profile, outputCodec, packagingConfig, sourcePath, outputDir, baseName, threads)
	tasks = append(tasks, topTask)
	addedHeights[topTask.Height] = true

	// walk down the ladder
	for _, height := range ladder {
		if height >= topHeight || addedHeights[height] {
			continue
		}

		taskWidth := int(math.Round(float64(height)*sourceAspectRatio/2.0)) * 2

		// skip rungs too small for GPU encoders
		if isGpuProfile && taskWidth < gpuMinRenditionWidth {
			log.Printf("SKIPPING: Rendition %dp (width %dpx) is below the minimum required width (%dpx) for GPU encoding.", height, taskWidth, gpuMinRenditionWidth)
			continue
		}

		task := generateAutomaticTask(height, sourceInfo, referenceBitrate, profile, outputCodec, packagingConfig, sourcePath, outputDir, baseName, threads)
		tasks = append(tasks, task)
		addedHeights[height] = true
	}

	log.Printf("Automatically generated %d video renditions.", len(tasks))
	return tasks
}

// generateAutomaticTask   one ladder rung as an EncodingTask.
func generateAutomaticTask(
	taskHeight int,
	sourceInfo *types.VideoAnalysis,
	referenceBitrate int,
	profile EncodingProfile,
	outputCodec string,
	packagingConfig vodTypes.Packaging,
	sourcePath string,
	outputDir string,
	baseName string,
	threads int,
) types.EncodingTask {
	sourceAspectRatio := float64(sourceInfo.Width) / float64(sourceInfo.Height)
	taskWidth := int(math.Round(float64(taskHeight)*sourceAspectRatio/2.0)) * 2

	finalCodec, hwAccel, pixFmt, codecProfile := getCodecSettings(profile, outputCodec, sourceInfo.IsHDR)

	targetBitrate, maxBitrate, bufferSize := calculateAutomaticBitrates(taskHeight, taskWidth, sourceInfo, referenceBitrate, profile)

	// don't let top rung maxrate dip below source reference
	if taskHeight == sourceInfo.Height && maxBitrate < referenceBitrate {
		log.Printf(">> QUALITY GUARD: Automatic maxrate (%dkbps) is lower than source reference (%dkbps). Adjusting to %dkbps.", maxBitrate, referenceBitrate, int(float64(referenceBitrate)*1.1))
		maxBitrate = int(float64(referenceBitrate) * 1.1)
	}

	rateControl := video.RateControl{}
	if isSoftwareEncoder(finalCodec) {
		rateControl.CRF = getQualityLevelForRendition(taskHeight)
		rateControl.VBR = &video.BitrateConfig{MaxBitrate: fmt.Sprintf("%dk", maxBitrate), BufferSize: fmt.Sprintf("%dk", bufferSize)}
	} else {
		rateControl.QP = getQualityLevelForRendition(taskHeight) + 2
		rateControl.VBR = &video.BitrateConfig{
			TargetBitrate: fmt.Sprintf("%dk", targetBitrate),
			MaxBitrate:    fmt.Sprintf("%dk", maxBitrate),
			BufferSize:    fmt.Sprintf("%dk", bufferSize),
		}
	}

	gopSize := int(math.Round(sourceInfo.FrameRate * float64(packagingConfig.SegmentDurationSeconds)))
	if gopSize == 0 {
		gopSize = int(math.Round(sourceInfo.FrameRate * 2.0))
	}

	hdrConfig := getHDRConfig(sourceInfo.IsHDR, finalCodec)

	// tonemap path always lands on 8-bit sdr
	if hdrConfig != nil && hdrConfig.ToneMap != "" {
		pixFmt = "yuv420p"
	}

	var codecExtraArgsList []string
	if finalCodec == "libx264" {
		codecExtraArgsList = append(codecExtraArgsList, "scenecut=0", "nal-hrd=cbr", "force-cfr=1")
	} else if finalCodec == "libx265" {
		codecExtraArgsList = append(codecExtraArgsList, "scenecut=0", "hrd-conformance=cbr")
	}
	codecExtraArgs := strings.Join(codecExtraArgsList, ":")

	return types.EncodingTask{
		Name:        baseName,
		InputFile:   sourcePath,
		OutputFile:  generateTaskOutputPath(outputDir, baseName, "video", strconv.Itoa(taskHeight), "", -1, "mp4"),
		HWAccel:     hwAccel,
		Codec:       finalCodec,
		RateControl: rateControl,
		GOP:         video.GOP{FPS: fmt.Sprintf("%.3f", sourceInfo.FrameRate), GOPSize: gopSize, KeyintMin: gopSize, BFrames: getBFramesForProfile(profile)},
		Params: video.Params{
			Tune:           getTuneForRendition(taskHeight, profile, outputCodec),
			Preset:         getPresetForRendition(taskHeight, profile),
			Profile:        codecProfile,
			Level:          "",
			PixelFormat:    pixFmt,
			CodecExtraArgs: codecExtraArgs,
		},
		VideoFilter: getVideoFilter(profile, taskWidth, taskHeight, hdrConfig, sourceInfo.IsHDR),
		Width:       taskWidth,
		Height:      taskHeight,
		HDR:         hdrConfig,
		Threads:     threads,
	}
}

func getReferenceBitrate(sourceBitrate int, sourceInfo *types.VideoAnalysis) int {
	if sourceBitrate > 0 {
		efficiencyFactor := CodecEfficiencyFactors[strings.ToLower(sourceInfo.CodecName)]
		if efficiencyFactor == 0 {
			efficiencyFactor = 1.0
		}
		refBitrate := int(float64(sourceBitrate) * efficiencyFactor)
		log.Printf("Using source bitrate %d kbps (codec: %s, efficiency: %.2f) as reference. Quality-matched H.264 bitrate: %d kbps.",
			sourceBitrate, sourceInfo.CodecName, efficiencyFactor, refBitrate)
		return refBitrate
	}

	presetBitrate, ok := BitratePresets[sourceInfo.Height]
	if !ok {
		for _, h := range MasterLadder {
			if sourceInfo.Height > h {
				presetBitrate = BitratePresets[h]
				break
			}
		}
		if presetBitrate == 0 {
			presetBitrate = BitratePresets[240]
		}
	}
	log.Printf("Warning: Source bitrate is unknown. Using preset bitrate of %d kbps for %dp resolution as reference.", presetBitrate, sourceInfo.Height)
	return presetBitrate
}

func calculateAutomaticBitrates(taskHeight, taskWidth int, sourceInfo *types.VideoAnalysis, refBitrate int, profile EncodingProfile) (target, max, buffer int) {
	if taskHeight >= sourceInfo.Height {
		target = int(float64(refBitrate) * fileSizeReductionFactor)
	} else {
		sourcePixels := float64(sourceInfo.Width * sourceInfo.Height)
		taskPixels := float64(taskWidth * taskHeight)
		bitrateRatio := math.Pow(taskPixels/sourcePixels, bitrateScalingExponent)
		target = int(float64(refBitrate) * bitrateRatio)
	}

	if target < minRenditionBitrateKbps {
		target = minRenditionBitrateKbps
	}

	if profile == ProfileCPUDefault {
		max = int(float64(target) * cpuMaxrateMultiplier)
		buffer = int(float64(max) * cpuBufsizeMultiplier)
	} else {
		max = int(float64(target) * gpuMaxrateMultiplier)
		buffer = int(float64(max) * gpuBufsizeMultiplier)
	}

	log.Printf("Generated automatic bitrate for %dp: Target=%dk, Max=%dk, Buffer=%dk", taskHeight, target, max, buffer)
	return
}

func getQualityLevelForRendition(height int) int {
	switch {
	case height >= 1080:
		return 21
	case height >= 720:
		return 22
	default:
		return 23
	}
}

func getHDRConfig(isSourceHDR bool, finalCodec string) *video.HDRConfig {
	if !isSourceHDR {
		return nil
	}
	if strings.Contains(finalCodec, "264") {
		log.Printf("INFO: Source is HDR and output encoder is '%s'. Applying automatic HDR->SDR tonemapping for compatibility.", finalCodec)
		return &video.HDRConfig{ToneMap: "hable"}
	}
	log.Printf("INFO: Source is HDR and output encoder is '%s'. Preserving HDR signal (no tonemapping).", finalCodec)
	return nil
}

func getVideoFilter(profile EncodingProfile, taskWidth, taskHeight int, hdrConfig *video.HDRConfig, isSourceHDR bool) string {
	isTonemapping := hdrConfig != nil && hdrConfig.ToneMap != ""
	var filters []string

	cpuAdvancedTonemapFilter := "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=gamma:param=1.2:desat=0:peak=15,zscale=p=709:t=709:m=709:r=full:d=error_diffusion,noise=alls=3:allf=t+u,eq=saturation=0.9:brightness=0.15:contrast=1.15:gamma=0.85,huesaturation=colors='y':saturation=-0.5:intensity=0.25,curves=all='0.05/0 0.35/0.5 1/1',curves=all='0/0 0.75/0.76 0.9/0.94 1/1',deband=1thr=0.015:2thr=0.015:3thr=0.015:4thr=0.015:range=16:blur=true:coupling=true,noise=alls=2:allf=p+t,colorspace=iall=bt709:all=bt709:range=tv:format=yuv420p:dither=fsb"

	switch profile {
	case ProfileNvidiaDefault:
		if isTonemapping {
			log.Println("Applying NVIDIA GPU HDR to SDR conversion.")
			filters = append(filters, "hwdownload")
			filters = append(filters, "format=p010le")
			filters = append(filters, "zscale=t=linear:npl=100,tonemap=tonemap=hable,zscale=p=709:t=709:m=709")
			filters = append(filters, fmt.Sprintf("scale=%d:%d", taskWidth, taskHeight))
			filters = append(filters, "format=nv12")
			filters = append(filters, "hwupload_cuda")
		} else if taskWidth > 0 && taskHeight > 0 {
			if isSourceHDR {
				log.Println("Applying NVIDIA GPU-native 'scale_cuda' for HDR resizing.")
				filters = append(filters, fmt.Sprintf("scale_cuda=w=%d:h=%d:format=p010le:interp_algo=lanczos", taskWidth, taskHeight))
			} else {
				log.Println("Applying NVIDIA GPU-native 'scale_cuda' for SDR resizing.")
				filters = append(filters, fmt.Sprintf("scale_cuda=w=%d:h=%d:format=nv12:interp_algo=lanczos", taskWidth, taskHeight))
			}
		}

	case ProfileIntelQSVDefault:
		log.Println("Applying Intel QSV workflow.")
		var vppOptions []string
		if taskWidth > 0 && taskHeight > 0 {
			vppOptions = append(vppOptions, fmt.Sprintf("w=%d:h=%d", taskWidth, taskHeight))
		}
		if isTonemapping {
			vppOptions = append(vppOptions, "tonemap=1")
		}
		if len(vppOptions) > 0 {
			filters = append(filters, fmt.Sprintf("vpp_qsv=%s", strings.Join(vppOptions, ":")))
		}

	case ProfileAMDDefault:
		log.Println("Applying AMD AMF workflow.")
		if isTonemapping {
			// AMF can't tonemap hdr→sdr natively, fall back to cpu chain
			log.Println("AMD AMF doesn't support native HDR->SDR conversion, using CPU tonemap.")
			filters = append(filters, "hwdownload")
			filters = append(filters, cpuAdvancedTonemapFilter)
			if taskWidth > 0 && taskHeight > 0 {
				filters = append(filters, fmt.Sprintf("scale=%d:%d", taskWidth, taskHeight))
			}
			filters = append(filters, "format=nv12")
			filters = append(filters, "hwupload")
		} else if taskWidth > 0 && taskHeight > 0 {
			if isSourceHDR {
				log.Println("Applying AMD GPU scaling for HDR content.")
				filters = append(filters, fmt.Sprintf("scale=%d:%d:format=p010le", taskWidth, taskHeight))
			} else {
				log.Println("Applying AMD GPU scaling for SDR content.")
				filters = append(filters, fmt.Sprintf("scale=%d:%d:format=nv12", taskWidth, taskHeight))
			}
		}

	case ProfileAppleDefault:
		log.Println("Applying Apple VideoToolbox workflow.")
		if isTonemapping {
			// videotoolbox hdr is kinda limited   cpu tonemap instead
			log.Println("VideoToolbox HDR support is limited, using CPU tonemap.")
			filters = append(filters, cpuAdvancedTonemapFilter)
			if taskWidth > 0 && taskHeight > 0 {
				filters = append(filters, fmt.Sprintf("scale=%d:%d", taskWidth, taskHeight))
			}
		} else if taskWidth > 0 && taskHeight > 0 {
			filters = append(filters, fmt.Sprintf("scale=%d:%d", taskWidth, taskHeight))
		}

	default:
		if isTonemapping {
			log.Println("Applying advanced CPU-only workflow with custom zscale/tonemap filter chain.")
			filters = append(filters, cpuAdvancedTonemapFilter)
			if taskWidth > 0 && taskHeight > 0 {
				filters = append(filters, fmt.Sprintf("scale=%d:%d", taskWidth, taskHeight))
			}
		} else if taskWidth > 0 && taskHeight > 0 {
			filters = append(filters, fmt.Sprintf("scale=%d:%d", taskWidth, taskHeight))
		}
	}

	return strings.Join(filters, ",")
}

func getPresetForRendition(height int, profile EncodingProfile) string {
	if profile == ProfileNvidiaDefault {
		return "p5"
	}
	if profile == ProfileIntelQSVDefault {
		return "medium"
	}
	if profile == ProfileAMDDefault {
		return "balanced"
	}
	if profile == ProfileAppleDefault {
		return "balanced"
	}

	return "medium"
}

func getTuneForRendition(height int, profile EncodingProfile, codec string) string {
	if profile != ProfileCPUDefault {
		return ""
	}
	if codec == "libx264" {
		return "film"
	}
	if codec == "libx265" {
		// libx265 doesn't have a 'film' tune   leave empty
		return ""
	}
	return ""
}

func getBFramesForProfile(profile EncodingProfile) int {
	if profile == ProfileCPUDefault {
		return 3
	}
	return 2
}
func isSoftwareEncoder(codec string) bool { return strings.HasPrefix(codec, "lib") }

func isHDR(stream types.FfprobeStream) bool {
	switch stream.ColorTransfer {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}

func parseFPS(frameRateStr string) float64 {
	if frameRateStr == "" || frameRateStr == "0/0" {
		return 24.0
	}
	parts := strings.Split(frameRateStr, "/")
	if len(parts) == 2 {
		num, errNum := strconv.ParseFloat(parts[0], 64)
		den, errDen := strconv.ParseFloat(parts[1], 64)
		if errNum == nil && errDen == nil && den != 0 {
			return num / den
		}
	}
	if fps, err := strconv.ParseFloat(frameRateStr, 64); err == nil {
		return fps
	}
	return 24.0
}
