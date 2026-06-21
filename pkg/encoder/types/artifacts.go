package types

// GIF job settings from analysis config.
type GIFSettings struct {
	Enable         bool      `json:"enable"`
	AllowSoftFail  bool      `json:"allow_soft_fail"`
	TimeRange      TimeRange `json:"time_range"`
	Dimensions     `json:"dimensions"`
	FrameRate      int    `json:"frame_rate"`
	OutputFilename string `json:"output_filename,omitempty"`
}

// Thumbnail job   mode picks vtt_sprite vs bif vs single_image.
type ThumbnailSettings struct {
	Enable           bool       `json:"enable"`
	AllowSoftFail    bool       `json:"allow_soft_fail,omitempty"`
	Mode             string     `json:"mode"` // vtt_sprite, bif, single_image
	IntervalSeconds  float64    `json:"interval_seconds,omitempty"`
	Timestamps       []float64  `json:"timestamps,omitempty"`
	Dimensions       Dimensions `json:"dimensions"`
	AspectMode       string     `json:"aspect_mode,omitempty"`  // stretch, pad, crop
	ImageFormat      string     `json:"image_format,omitempty"` // jpg or webp
	Quality          int        `json:"quality,omitempty"`
	FilenamePattern  string     `json:"filename_pattern,omitempty"`
	ManifestFilename string     `json:"manifest_filename,omitempty"`
	SpriteFilename   string     `json:"sprite_filename,omitempty"`
}

type TimeRange struct {
	StartSeconds    float64 `json:"start_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
}
