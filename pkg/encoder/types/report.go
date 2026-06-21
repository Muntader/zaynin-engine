package types

import (
	"github.com/muntader/zaynin-engine/pkg/encoder/media/audio"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/subtitle"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/video"
)

// What analyze spits out   source info plus ready-to-run tasks.
type AnalysisReport struct {
	Source    FfprobeFormat   `json:"source"`
	Video     VideoAnalysis   `json:"video"`
	Audio     []audio.Task    `json:"audio"`
	Subtitles []subtitle.Task `json:"subtitles"`
}

type VideoAnalysis struct {
	HasVideo    bool           `json:"has_video"`
	SourceIndex int            `json:"source_index"`
	CodecName   string         `json:"codec_name"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	FrameRate   float64        `json:"frame_rate"`
	IsHDR       bool           `json:"is_hdr"`
	Renditions  []EncodingTask `json:"renditions"`
}

// One ffmpeg video encode   maps to a ladder rung or custom rendition.
type EncodingTask struct {
	Name        string            `json:"name"`
	InputFile   string            `json:"input_file"`
	OutputFile  string            `json:"output_file"`
	Codec       string            `json:"codec"`
	HWAccel     string            `json:"hwaccel,omitempty"`
	RateControl video.RateControl `json:"rate_control"`
	HDR         *video.HDRConfig  `json:"hdr,omitempty"`
	GOP         video.GOP         `json:"gop"`
	Params      video.Params      `json:"params,omitempty"`
	VideoFilter string            `json:"video_filter"`
	NoAudio     bool              `json:"no_audio"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	Threads     int               `json:"threads"`
}

// Subtitle task at the report layer (convert_to_vtt or burn_in).
type SubtitleTask struct {
	InputFile    string `json:"input_file"`
	OutputFile   string `json:"output_file"`
	SourceIndex  int    `json:"source_index"`
	Language     string `json:"language"`
	Label        string `json:"label"`
	IsImageBased bool   `json:"is_image_based"`
	Action       string `json:"action"`
}
