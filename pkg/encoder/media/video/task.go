package video

import (
	"errors"
	"fmt"
	"log"
	"math"
	"runtime"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/cpu"
)

type RateControl struct {
	QP  int            `json:"qp,omitempty"`
	CRF int            `json:"crf,omitempty"`
	VBR *BitrateConfig `json:"vbr,omitempty"`
	CBR *BitrateConfig `json:"cbr,omitempty"`
}

type GOP struct {
	FPS       string `json:"fps,omitempty"`
	GOPSize   int    `json:"gop_size,omitempty"`
	KeyintMin int    `json:"keyint_min,omitempty"`
	BFrames   int    `json:"b_frames,omitempty"`
}

type Params struct {
	Tune           string `json:"tune,omitempty"`
	Preset         string `json:"preset,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Level          string `json:"level,omitempty"`
	PixelFormat    string `json:"pixel_format,omitempty"`
	CodecExtraArgs string `json:"codec_extra_args,omitempty"`
}

type BitrateConfig struct {
	Passes        int    `json:"passes"`
	TargetBitrate string `json:"target_bitrate"`
	MaxBitrate    string `json:"max_bitrate,omitempty"`
	BufferSize    string `json:"buffer_size,omitempty"`
}

// Segment alignment knobs for packaging.
type ABRConfig struct {
	SegmentDuration float64 `json:"segment_duration"`
	FPS             float64 `json:"fps"`
}

type HDRConfig struct {
	ToneMap     string `json:"tone_map,omitempty"` // hable, mobius:npl=200, etc
	Passthrough bool   `json:"passthrough,omitempty"`
}

type Task struct {
	InputFile             string      `json:"input_file"`
	OutputFile            string      `json:"output_file"`
	HWAccel               string      `json:"hwaccel,omitempty"`
	Codec                 string      `json:"codec"`
	RateControl           RateControl `json:"rate_control"`
	ABR                   *ABRConfig  `json:"abr,omitempty"`
	HDR                   *HDRConfig  `json:"hdr,omitempty"`
	GOP                   GOP         `json:"gop,omitempty"`
	Params                Params      `json:"params,omitempty"`
	VideoFilter           string      `json:"video_filter,omitempty"`
	NoAudio               bool        `json:"no_audio,omitempty"`
	Width                 int         `json:"width,omitempty"`
	Height                int         `json:"height,omitempty"`
	Threads               int         `json:"threads,omitempty"` // 0 = auto
	ForcedSegmentDuration float64     `json:"-"`
}

// Pick thread count   honor user override, else don't hog the whole machine.
func (t *Task) calculateThreads() int {
	if t.Threads > 0 {
		log.Printf("INFO: User has specified %d threads.", t.Threads)
		return t.Threads
	}

	logicalCores := runtime.NumCPU()
	physicalCores, err := cpu.Counts(false)

	if err != nil {
		log.Printf("WARN: Could not detect physical CPU cores: %v. Falling back to logical core-based threading.", err)
		var threadsToUse int
		if logicalCores <= 4 {
			threadsToUse = logicalCores / 2
		} else {
			threadsToUse = 4
		}
		if threadsToUse < 1 {
			threadsToUse = 1
		}
		log.Printf("INFO: Fallback based on %d logical cores. Setting thread limit to %d.", logicalCores, threadsToUse)
		return threadsToUse
	}

	var targetPhysicalCores int
	if physicalCores <= 4 {
		targetPhysicalCores = physicalCores / 2
	} else {
		targetPhysicalCores = 4 // cap at 4 physical on bigger boxes
	}

	if targetPhysicalCores < 1 {
		targetPhysicalCores = 1
	}

	threadsToUse := targetPhysicalCores * 2 // HT → 2 threads per core

	if threadsToUse > logicalCores {
		threadsToUse = logicalCores
	}

	log.Printf("INFO: Detected %d physical cores (%d logical threads). Targeting %d physical cores, setting thread limit to %d.", physicalCores, logicalCores, targetPhysicalCores, threadsToUse)
	return threadsToUse
}

func (t *Task) ToArgs() []string {
	args := []string{"-y"}

	// hw encoders manage their own threads   only clamp software codecs
	if isSoftwareEncoder(t.Codec) {
		threads := t.calculateThreads()
		threadStr := strconv.Itoa(threads)
		args = append(args, "-threads", threadStr)
		args = append(args, "-filter_threads", threadStr)
	}

	isOclTonemap := strings.Contains(t.VideoFilter, "tonemap_opencl")

	if isOclTonemap {
		log.Println("INFO: OpenCL tonemapping detected. Prepending OpenCL and CUDA HW flags.")
		args = append(args, "-init_hw_device", "opencl=ocl", "-filter_hw_device", "ocl")
		args = append(args, "-hwaccel", "cuda")

	} else if t.HWAccel != "" && t.HWAccel != "cpu" {
		log.Printf("INFO: Applying standard hardware acceleration: %s", t.HWAccel)
		args = append(args, "-hwaccel", t.HWAccel)
		if t.HWAccel == "cuda" {
			args = append(args, "-hwaccel_output_format", "cuda")
		}
	}

	args = append(args, "-i", t.InputFile, "-c:v", t.Codec)

	args = append(args, t.RateControl.ToArgs(t.Codec)...)
	args = append(args, t.Params.ToArgs(t.Codec)...)
	args = append(args, t.buildGOPArgs()...)
	args = append(args, t.buildFilterArgs()...)

	if t.HDR != nil && t.HDR.Passthrough {
		args = append(args, t.HDR.ToArgs()...)
	}

	args = append(args, "-an")

	if strings.HasSuffix(t.OutputFile, ".mp4") {
		args = append(args, "-movflags", "+faststart")
	}

	return args
}

func (t *Task) buildGOPArgs() []string {
	var args []string

	if t.ForcedSegmentDuration > 0 {
		fps, err := strconv.ParseFloat(t.GOP.FPS, 64)
		if err != nil {
			log.Printf("WARN: Could not parse GOP.FPS value '%s'. Fixed GOP alignment may be incorrect.", t.GOP.FPS)
			return t.GOP.ToArgs()
		}

		gopSize := int(math.Round(fps * t.ForcedSegmentDuration))
		forceKeyFramesExpr := fmt.Sprintf("expr:gte(t,n_forced*%g)", t.ForcedSegmentDuration)

		log.Printf("INFO: Enforcing fixed GOP with segment duration %gs. Calculated GOP size: %d", t.ForcedSegmentDuration, gopSize)

		args = append(args,
			"-r", t.GOP.FPS,
			"-g", strconv.Itoa(gopSize),
			"-keyint_min", strconv.Itoa(gopSize),
			"-force_key_frames", forceKeyFramesExpr,
		)
	} else {
		log.Println("INFO: Using GOP settings directly from JSON file (fixed alignment disabled).")
		args = append(args, t.GOP.ToArgs()...)
	}

	if t.GOP.BFrames >= 0 {
		args = append(args, "-bf", strconv.Itoa(t.GOP.BFrames))
	}

	return args
}

func (t *Task) buildFilterArgs() []string {
	if t.VideoFilter != "" {
		log.Printf("INFO: Using generated video filter chain: %s", t.VideoFilter)
		return []string{"-vf", t.VideoFilter}
	}
	return nil
}

func (rc *RateControl) ToArgs(codec string) []string {
	var args []string
	if (rc.CRF > 0 || rc.QP > 0) && rc.VBR != nil {
		capsOnlyConf := *rc.VBR
		capsOnlyConf.TargetBitrate = ""
		if rc.CRF > 0 {
			args = append(args, "-crf", strconv.Itoa(rc.CRF))
		} else {
			if isNvidiaEncoder(codec) {
				args = append(args, "-rc", "vbr", "-cq", strconv.Itoa(rc.QP))
			} else {
				args = append(args, "-global_quality", strconv.Itoa(rc.QP))
			}
		}
		args = append(args, capsOnlyConf.ToArgs(codec)...)
		return args
	}
	if rc.VBR != nil {
		return rc.VBR.ToArgs(codec)
	}
	if rc.CBR != nil {
		return rc.CBR.ToArgs(codec)
	}
	if rc.CRF > 0 {
		return []string{"-crf", strconv.Itoa(rc.CRF)}
	}
	if rc.QP > 0 {
		if isNvidiaEncoder(codec) {
			return []string{"-rc", "constqp", "-cq", strconv.Itoa(rc.QP)}
		}
		return []string{"-global_quality", strconv.Itoa(rc.QP)}
	}
	return nil
}

func (g *GOP) ToArgs() []string {
	var args []string
	if g.FPS != "" {
		args = append(args, "-r", g.FPS)
	}
	if g.GOPSize > 0 {
		args = append(args, "-g", strconv.Itoa(g.GOPSize))
	}
	if g.KeyintMin > 0 {
		args = append(args, "-keyint_min", strconv.Itoa(g.KeyintMin))
	}
	return args
}

func (p *Params) ToArgs(codec string) []string {
	var args []string

	if p.Tune != "" {
		args = append(args, "-tune", p.Tune)
	}
	if p.Preset != "" {
		args = append(args, "-preset", p.Preset)
	}
	if p.Profile != "" {
		args = append(args, "-profile:v", p.Profile)
	}
	if p.Level != "" {
		args = append(args, "-level", p.Level)
	}
	if p.PixelFormat != "" && !isNvidiaEncoder(codec) {
		args = append(args, "-pix_fmt", p.PixelFormat)
	}

	if codec == "libx264" {
		if p.CodecExtraArgs != "" {
			args = append(args, "-x264-params", p.CodecExtraArgs)
		}
	} else if codec == "libx265" {
		var x265Params []string
		if p.CodecExtraArgs != "" {
			x265Params = append(x265Params, p.CodecExtraArgs)
		}
		x265Params = append(x265Params, "wpp")

		args = append(args, "-x265-params", strings.Join(x265Params, ":"))
	}

	return args
}

func (bc *BitrateConfig) ToArgs(codec string) []string {
	var args []string
	isNv := isNvidiaEncoder(codec)
	if isNv && bc.TargetBitrate != "" {
		if bc.MaxBitrate != "" {
			args = append(args, "-rc", "vbr")
		} else {
			args = append(args, "-rc", "cbr")
		}
	}
	if bc.TargetBitrate != "" {
		args = append(args, "-b:v", bc.TargetBitrate)
	}
	if bc.MaxBitrate != "" {
		args = append(args, "-maxrate", bc.MaxBitrate)
	}
	if bc.BufferSize != "" {
		args = append(args, "-bufsize", bc.BufferSize)
	}
	if isNv && bc.Passes > 1 {
		args = append(args, "-multipass", "qres")
	}
	return args
}

func (h *HDRConfig) ToArgs() []string {
	if h.Passthrough {
		return []string{
			"-colorspace", "bt2020nc",
			"-color_primaries", "bt2020",
			"-color_trc", "smpte2084",
		}
	}
	return nil
}

func (t *Task) Validate() error {
	return firstError(
		t.validateRequiredFields,
		t.validateRateControl,
		t.validateABR,
		t.validateHardware,
		t.validateCodecParams,
		t.validateHDR,
	)
}

func (t *Task) validateRequiredFields() error {
	if t.InputFile == "" {
		return errors.New("field 'input_file' is required")
	}
	if t.OutputFile == "" {
		return errors.New("field 'output_file' is required")
	}
	if t.Codec == "" {
		return errors.New("field 'codec' is required")
	}
	return nil
}

func (t *Task) validateRateControl() error {
	rc := t.RateControl
	isQP := rc.QP > 0
	isCRF := rc.CRF > 0
	isVBR := rc.VBR != nil
	isCBR := rc.CBR != nil

	if (isCRF || isQP) && isVBR {
		if isCRF && isQP {
			return errors.New("cannot specify both CRF and QP simultaneously")
		}
		if isCRF && !isSoftwareEncoder(t.Codec) {
			return fmt.Errorf("CRF is not supported by hardware codec '%s'; use QP", t.Codec)
		}
		if isQP && isSoftwareEncoder(t.Codec) {
			return fmt.Errorf("QP is not the standard quality mode for software encoder '%s'; use CRF", t.Codec)
		}
		return nil
	}

	modeCount := 0
	if isQP {
		modeCount++
	}
	if isCRF {
		modeCount++
	}
	if isVBR {
		modeCount++
	}
	if isCBR {
		modeCount++
	}

	if modeCount == 0 {
		return errors.New("no rate control mode specified (e.g., CRF, VBR)")
	}
	if modeCount > 1 {
		return errors.New("multiple exclusive rate control modes specified; use only one of CRF, QP, VBR, or CBR")
	}
	if isVBR && rc.VBR.TargetBitrate == "" {
		return errors.New("pure VBR mode requires a 'target_bitrate'")
	}
	if isCBR && rc.CBR.TargetBitrate == "" {
		return errors.New("CBR mode requires a 'target_bitrate'")
	}
	return nil
}

func (t *Task) validateABR() error {
	if t.ABR != nil {
		if t.ABR.FPS <= 0 {
			return errors.New("abr.fps must be a positive number")
		}
		if t.ABR.SegmentDuration <= 0 {
			return errors.New("abr.segment_duration must be a positive number")
		}
	}
	return nil
}

func (t *Task) validateHardware() error {
	if t.isHardwareEncoder() && t.HWAccel == "" {
		return fmt.Errorf("codec '%s' is a hardware encoder, but 'hwaccel' is not specified", t.Codec)
	}
	return nil
}

func (t *Task) validateCodecParams() error {
	return nil
}

func (t *Task) validateHDR() error {
	if t.HDR == nil {
		return nil
	}
	if t.HDR.ToneMap != "" && t.HDR.Passthrough {
		return errors.New("cannot specify both 'hdr.tone_map' and 'hdr.passthrough'; choose one")
	}
	if t.HDR.Passthrough && !strings.Contains(t.Codec, "hevc") {
		return fmt.Errorf("HDR passthrough is typically only useful with HDR-capable codecs like hevc_nvenc, not '%s'", t.Codec)
	}
	return nil
}

func firstError(validators ...func() error) error {
	for _, v := range validators {
		if err := v(); err != nil {
			return err
		}
	}
	return nil
}

func (t *Task) isHardwareEncoder() bool {
	return isNvidiaEncoder(t.Codec) || strings.Contains(t.Codec, "_qsv") || strings.Contains(t.Codec, "_amf") || strings.Contains(t.Codec, "videotoolbox")
}

func isSoftwareEncoder(codec string) bool {
	return strings.HasPrefix(codec, "lib")
}

func isNvidiaEncoder(codec string) bool {
	return strings.HasSuffix(codec, "_nvenc")
}
