package types

// Width/height for outputs. 0 or -1 means figure it out.
type Dimensions struct {
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

type AudioContentType string

const (
	AudioContentSpeech     AudioContentType = "speech"
	AudioContentMusic      AudioContentType = "music"
	AudioContentSurround   AudioContentType = "surround"
	AudioContentCommentary AudioContentType = "commentary"
	AudioContentGeneral    AudioContentType = "general"
)

type AudioQuality string

const (
	AudioQualityHigh    AudioQuality = "high"
	AudioQualityMedium  AudioQuality = "medium"
	AudioQualityLow     AudioQuality = "low"
	AudioQualityUnknown AudioQuality = "unknown"
)

type SubtitleType string

const (
	SubtitleTypeForced  SubtitleType = "forced"
	SubtitleTypeFull    SubtitleType = "full"
	SubtitleTypeSDH     SubtitleType = "sdh"
	SubtitleTypeCC      SubtitleType = "cc"
	SubtitleTypeRegular SubtitleType = "regular"
)

// Parsed ffprobe disposition flags.
type StreamDisposition struct {
	Default         bool `json:"default"`
	Dub             bool `json:"dub"`
	Original        bool `json:"original"`
	Comment         bool `json:"comment"`
	Lyrics          bool `json:"lyrics"`
	Karaoke         bool `json:"karaoke"`
	Forced          bool `json:"forced"`
	HearingImpaired bool `json:"hearing_impaired"`
	VisualImpaired  bool `json:"visual_impaired"`
	CleanEffects    bool `json:"clean_effects"`
	AttachedPic     bool `json:"attached_pic"`
	TimedThumbnails bool `json:"timed_thumbnails"`
}
