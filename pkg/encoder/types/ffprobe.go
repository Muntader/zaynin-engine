package types

import (
	"fmt"
	"strconv"
)

// Root of ffprobe -show_format -show_streams JSON.
type FfprobeOutput struct {
	Format  FfprobeFormat   `json:"format"`
	Streams []FfprobeStream `json:"streams"`
}

type FfprobeFormat struct {
	Filename string `json:"filename"`
	Duration string `json:"duration"`
	BitRate  string `json:"bit_rate"`
	Size     string `json:"size"`
}

type FfprobeStream struct {
	Index         int               `json:"index"`
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Width         int               `json:"width,omitempty"`
	Height        int               `json:"height,omitempty"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	BitRate       string            `json:"bit_rate,omitempty"`
	Channels      int               `json:"channels,omitempty"`
	SampleRate    string            `json:"sample_rate,omitempty"`
	ColorTransfer string            `json:"color_transfer,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	Disposition   map[string]int    `json:"disposition,omitempty"`
}

// ffprobe -show_frames output.
type FfprobeFrameOutput struct {
	Frames []FfprobeFrame `json:"frames"`
}

type FfprobeFrame struct {
	SideDataList []FrameSideData `json:"side_data_list,omitempty"`
}

type FrameSideData struct {
	SideDataType string `json:"side_data_type"`
	MaxCLL       string `json:"max_cll,omitempty"`
	MaxFALL      string `json:"max_fall,omitempty"`
	MaxContent   string `json:"max_content,omitempty"`
}

// HDR bits we actually care about from frame side data.
type HDRFrameInfo struct {
	HasMasteringDisplay  bool
	HasContentLightLevel bool
	MaxCLL               int
}

func (f *FfprobeOutput) GetFirstVideoStream() *FfprobeStream {
	for i := range f.Streams {
		if f.Streams[i].CodecType == "video" {
			return &f.Streams[i]
		}
	}
	return nil
}

func (f *FfprobeFormat) DurationSeconds() (float64, error) {
	if f.Duration == "" {
		return 0, fmt.Errorf("ffprobe format data does not contain a duration")
	}

	duration, err := strconv.ParseFloat(f.Duration, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse duration string %q: %w", f.Duration, err)
	}

	return duration, nil
}
