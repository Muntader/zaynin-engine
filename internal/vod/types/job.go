package types


// Config is the job JSON the API accepts and workers carry through the pipeline.
type Config struct {
	JobLabel        string           `json:"job_label,omitempty"`
	JobID           string           `json:"job_id" validate:"required"`
	InputStorage    InputStorage     `json:"input_storage" validate:"required"`
	OutputStorage   OutputStorage    `json:"output_storage" validate:"required"`
	JobSettings     *JobSettings     `json:"job_settings,omitempty" validate:"omitempty"`
	Outputs         Outputs          `json:"outputs" validate:"required"`
	DeliveryOptions *DeliveryOptions `json:"delivery_options,omitempty"`
}

// DeliveryOptions controls what extra stuff lands in the final upload bundle.
type DeliveryOptions struct {
	KeepSourceVideo bool   `json:"keep_source_video,omitempty"`
	KeepSourceName  string `json:"keep_source_name,omitempty" validate:"required_if=KeepSourceVideo true"`
	IncludeJobConfig            bool `json:"include_job_config,omitempty"`             // handy for debugging downstream issues
	IncludeSourceAnalysisReport bool `json:"include_source_analysis_report,omitempty"` // ffprobe dump for support tickets
}

// InputStorage wraps providers so validation can enforce exactly one source.
type InputStorage struct {
	InputID  string      `json:"input_id" validate:"required"`
	Provider string      `json:"provider" validate:"required,oneof=s3 gcs azure r2 http sftp local"`
	HTTP     *InputHTTP  `json:"http,omitempty" validate:"omitempty,required_without_all=S3 GCS Azure R2 SFTP"`
	S3       *InputS3    `json:"s3,omitempty" validate:"omitempty,required_without_all=HTTP GCS Azure R2 SFTP"`
	GCS      *InputGCS   `json:"gcs,omitempty" validate:"omitempty,required_without_all=HTTP S3 Azure R2 SFTP"`
	Azure    *InputAzure `json:"azure,omitempty" validate:"omitempty,required_without_all=HTTP S3 GCS R2 SFTP"`
	R2       *InputR2    `json:"r2,omitempty" validate:"omitempty,required_without_all=HTTP S3 GCS Azure SFTP"`
	SFTP     *InputSFTP  `json:"sftp,omitempty" validate:"omitempty,required_without_all=HTTP S3 GCS Azure R2"`
	Local    *InputLocal `json:"local,omitempty"`
}

type InputLocal struct {
	Path string `json:"path"`
}

type InputHTTP struct {
	URL string `json:"url" validate:"required,url"`
}

type InputS3 struct {
	Bucket string `json:"bucket" validate:"required"`
	Key    string `json:"key" validate:"required"`
	Region string `json:"region" validate:"required"`
	Credentials *AWSCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type InputGCS struct {
	Bucket string `json:"bucket" validate:"required"`
	Key    string `json:"key" validate:"required"`
	Credentials *GCSCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type InputAzure struct {
	Container string `json:"container" validate:"required"`
	Key       string `json:"key" validate:"required"` // blob name, not a path prefix
	Credentials *AzureCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type InputR2 struct {
	Bucket      string         `json:"bucket" validate:"required"`
	Key         string         `json:"key" validate:"required"`
	EndpointURL string         `json:"endpoint_url" validate:"required,url"`
	Credentials *R2Credentials `json:"credentials,omitempty" validate:"omitempty"`
}

type InputSFTP struct {
	Path        string           `json:"path" validate:"required"`
	Credentials *SFTPCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

// OutputStorage mirrors InputStorage so upload routing stays symmetric with download.
type OutputStorage struct {
	OutputID string       `json:"output_id" validate:"required"`
	Provider string       `json:"provider" validate:"required,oneof=s3 gcs azure r2 sftp local"`
	S3       *OutputS3    `json:"s3,omitempty" validate:"omitempty,required_without_all=GCS Azure R2 SFTP"`
	GCS      *OutputGCS   `json:"gcs,omitempty" validate:"omitempty,required_without_all=S3 Azure R2 SFTP"`
	Azure    *OutputAzure `json:"azure,omitempty" validate:"omitempty,required_without_all=S3 GCS R2 SFTP"`
	R2       *OutputR2    `json:"r2,omitempty" validate:"omitempty,required_without_all=S3 GCS Azure SFTP"`
	SFTP     *OutputSFTP  `json:"sftp,omitempty" validate:"omitempty,required_without_all=S3 GCS Azure R2"`
	HTTP     *OutputHTTP  `json:"http,omitempty" validate:"omitempty,required_without_all=S3 GCS Azure R2 SFTP"`
	Local    *OutputLocal `json:"local,omitempty"`
}

type OutputS3 struct {
	Bucket      string          `json:"bucket" validate:"required"`
	Key         string          `json:"key" validate:"required"` // upload prefix, not a single object key
	Region      string          `json:"region" validate:"required"`
	Credentials *AWSCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type OutputGCS struct {
	Bucket      string          `json:"bucket" validate:"required"`
	Key         string          `json:"key" validate:"required"` // upload prefix
	Credentials *GCSCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type OutputAzure struct {
	Container   string            `json:"container" validate:"required"`
	Key         string            `json:"key" validate:"required"` // upload prefix
	Credentials *AzureCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type OutputR2 struct {
	Bucket      string         `json:"bucket" validate:"required"`
	Key         string         `json:"key" validate:"required"` // upload prefix
	EndpointURL string         `json:"endpoint_url" validate:"required,url"`
	Credentials *R2Credentials `json:"credentials,omitempty" validate:"omitempty"`
}

type OutputSFTP struct {
	Path        string           `json:"path" validate:"required"` // base dir on the remote host
	Credentials *SFTPCredentials `json:"credentials,omitempty" validate:"omitempty"`
}

type OutputHTTP struct {
	URL     string            `json:"url" validate:"required,url"`
	Token   string            `json:"token,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type OutputLocal struct {
	Path string `json:"path"`
}

type AWSCredentials struct {
	AccessKeyID     string `json:"access_key_id" validate:"required"`
	SecretAccessKey string `json:"secret_access_key" validate:"required"`
}

type R2Credentials struct {
	AccessKeyID     string `json:"access_key_id" validate:"required"`
	SecretAccessKey string `json:"secret_access_key" validate:"required"`
}

type GCSCredentials struct {
	ServiceAccountJSON string `json:"service_account_json" validate:"required"`
}

type AzureCredentials struct {
	SASToken string `json:"sas_token" validate:"required"`
}

type SFTPCredentials struct {
	User       string `json:"user" validate:"required"`
	Password   string `json:"password,omitempty" validate:"required_without=PrivateKey,omitempty"`
	PrivateKey string `json:"private_key,omitempty" validate:"required_without=Password,omitempty"`
	Host       string `json:"host" validate:"required"`
	Port       int    `json:"port" validate:"required,gt=0,lte=65535"`
}

// Notifications is where we POST job status updates.
type Notifications struct {
	WebhookURL string `json:"webhook_url" validate:"required,url"`
	AuthToken  string `json:"auth_token,omitempty"`
}

// JobSettings tweaks encoder behavior per job (hwaccel etc).
type JobSettings struct {
	HardwareAcceleration string `json:"hardware_acceleration,omitempty"`
}

// Outputs groups everything the customer asked us to produce.
type Outputs struct {
	StreamingPackage *StreamingPackage `json:"streaming_package,omitempty"`
	Thumbnails       []Thumbnail       `json:"thumbnails,omitempty" validate:"omitempty,dive"`
	AnimatedGIFs     []AnimatedGIF     `json:"animated_gifs,omitempty" validate:"omitempty,dive"`
	Clips            []Clip            `json:"clips,omitempty" validate:"omitempty,dive"`
}

// StreamingPackage is the ABR ladder + packaging section of a job.
type StreamingPackage struct {
	Enable        bool       `json:"enable"`
	AllowSoftFail bool       `json:"allow_soft_fail,omitempty"`
	AllowUpscale  bool       `json:"allow_upscale,omitempty"`
	Video         Video      `json:"video" validate:"required_if=Enable true"`
	Audio         *Audio     `json:"audio" validate:"required_if=Enable true"`
	Subtitles     *Subtitles `json:"subtitles" validate:"required_if=Enable true"`
	Packaging     Packaging  `json:"packaging" validate:"required_if=Enable true"`
}

type Video struct {
	Encoder     string            `json:"encoder,omitempty"`
	RateControl *VideoRateControl `json:"rate_control,omitempty"`
	Threads     int               `json:"threads,omitempty"`
	Renditions  []VideoRendition  `json:"renditions" validate:"required,gte=0,dive"`
}

type VideoRendition struct {
	Tag         string            `json:"tag"`
	Height      int               `json:"height" validate:"gt=0"`
	Encoder     string            `json:"encoder" validate:"required"`
	Preset      string            `json:"preset,omitempty"`
	Profile     string            `json:"profile,omitempty"`
	Level       string            `json:"level,omitempty"`
	RateControl *VideoRateControl `json:"rate_control"`
	Tune        string            `json:"tune,omitempty"`
	Filtering   *Filtering        `json:"filtering,omitempty" validate:"omitempty"`
}

type VideoRateControl struct {
	Mode       string `json:"mode" validate:"required,oneof=cbr vbr abr"`
	Bitrate    string `json:"bitrate,omitempty"`
	MaxBitrate string `json:"max_bitrate,omitempty"`
	BufferSize string `json:"buffer_size,omitempty"`
	QPValue    int    `json:"qp_value,omitempty"`
	CRFValue   int    `json:"crf_value,omitempty"`
}

type Filtering struct {
	HdrToSdr HdrToSdr `json:"hdr_to_sdr,omitempty"`
}

type HdrToSdr struct {
	Enable           bool   `json:"enable"`
	TonemapAlgorithm string `json:"tonemap_algorithm,omitempty"`
}

type Audio struct {
	Mode          string         `json:"mode" validate:"required,oneof=copy passthrough custom auto"`
	Normalization *Normalization `json:"normalization,omitempty" validate:"omitempty"`
	Tracks        []AudioTrack   `json:"tracks,omitempty" validate:"omitempty,dive"`
}

type Normalization struct {
	Enable     bool    `json:"enable"`
	TargetLUFS float64 `json:"target_lufs" validate:"required_if=Enable true"`
}

type AudioTrack struct {
	Select AudioSelect `json:"select" validate:"required"`
	Output AudioOutput `json:"output" validate:"required"`
}

type AudioSelect struct {
	Language string `json:"language" validate:"required"`
	Channels int    `json:"channels,omitempty" validate:"omitempty,gt=0"`
}

type AudioOutput struct {
	Codec      string `json:"codec" validate:"required"`
	Bitrate    string `json:"bitrate" validate:"required"`
	Label      string `json:"label" validate:"required"`
	IsDefault  bool   `json:"is_default,omitempty"`
	Channels   int    `json:"channels,omitempty" validate:"omitempty,gt=0"`
	SampleRate int    `json:"sample_rate,omitempty" validate:"omitempty,gt=0"`
}

type Subtitles struct {
	Mode   string          `json:"mode" validate:"required,oneof=copy passthrough burn embed custom auto"`
	Tracks []SubtitleTrack `json:"tracks,omitempty" validate:"omitempty,dive"`
}

type SubtitleTrack struct {
	Select SubtitleSelect `json:"select" validate:"required"`
	Action string         `json:"action" validate:"required,oneof=include burn convert_to_vtt burn_in"`
	Label  string         `json:"label,omitempty"`
}

type SubtitleSelect struct {
	Language string `json:"language" validate:"required"`
	Forced   bool   `json:"forced,omitempty"`
}

type Packaging struct {
	SegmentDurationSeconds int          `json:"segment_duration_seconds" validate:"gt=0"`
	Formats                []string     `json:"formats" validate:"required,gt=0,dive,oneof=hls dash"`
	HLSSettings            *HLSSettings `json:"hls_settings,omitempty" validate:"omitempty"`
	DRM                    *DRM         `json:"drm,omitempty" validate:"omitempty"`
}

type HLSSettings struct {
	Container string `json:"container" validate:"required,oneof=fmp4 ts"`
	Version   int    `json:"version" validate:"gte=3"`
}

type DRM struct {
	Enable     bool         `json:"enable"`
	ContentID  string       `json:"content_id" validate:"required_if=Enable true"`
	Provider   *DRMProvider `json:"provider" validate:"required_if=Enable true"`
	Dash       *DRMDash     `json:"dash" validate:"required_if=Enable true"`
	HLS        *DRMHLS      `json:"hls" validate:"required_if=Enable true"`
	StaticKeys []string     `json:"static_keys,omitempty" validate:"omitempty,dive,required"`
}

type DRMProvider struct {
	Type   string                 `json:"type" validate:"required"`
	Config map[string]interface{} `json:"config" validate:"required"`
}

type DRMDash struct {
	Systems []string `json:"systems" validate:"required,gt=0,dive,required"`
}

type DRMHLS struct {
	Systems []string `json:"systems" validate:"required,gt=0,dive,required"`
}

type Thumbnail struct {
	ID              string     `json:"id"`
	Enable          bool       `json:"enable"`
	Mode            string     `json:"mode"`                       // single_image | vtt_sprite | interval
	Timestamps      []float64  `json:"timestamps,omitempty"`       // for single_image
	IntervalSeconds int        `json:"interval_seconds,omitempty"` // for interval / sprite
	Dimensions      Dimensions `json:"dimensions"`
	Quality         int        `json:"quality"`
	ImageFormat     string     `json:"image_format"`
	FilenamePattern string     `json:"filename_pattern"`
	AllowSoftFail   bool       `json:"allow_soft_fail,omitempty"`
	OutputSubdir    string     `json:"output_subdir,omitempty"` // keeps multiple thumb jobs from stepping on each other
}

type Dimensions struct {
	Width  int `json:"width,omitempty" validate:"omitempty,gt=0"`
	Height int `json:"height,omitempty" validate:"omitempty,gt=0"`
}

type AnimatedGIF struct {
	ID             string     `json:"id"`
	Enable         bool       `json:"enable"`
	AllowSoftFail  bool       `json:"allow_soft_fail,omitempty"`
	TimeRange      TimeRange  `json:"time_range"`
	Dimensions     Dimensions `json:"dimensions"`
	FrameRate      int        `json:"frame_rate"`
	OutputFilename string     `json:"output_filename"`
	OutputSubdir   string     `json:"output_subdir,omitempty"`
}

type TimeRange struct {
	StartSeconds    int `json:"start_seconds" validate:"gte=0"`
	DurationSeconds int `json:"duration_seconds" validate:"required,gt=0"`
}

type Clip struct {
	ID             string            `json:"id"`
	Enable         bool              `json:"enable"`
	TimeRange      TimeRange         `json:"time_range"`
	OutputFormat   string            `json:"output_format"` // mp4, webm, etc.
	VideoSettings  VideoClipSettings `json:"video_settings,omitempty"`
	AudioSettings  AudioClipSettings `json:"audio_settings,omitempty"`
	OutputFilename string            `json:"output_filename"`
	OutputSubdir   string            `json:"output_subdir,omitempty"`
}

type VideoClipSettings struct {
	Encoder    string      `json:"encoder"`
	Preset     string      `json:"preset,omitempty"`
	CRF        *int        `json:"crf,omitempty"`
	Dimensions *Dimensions `json:"dimensions,omitempty"`
}

type AudioClipSettings struct {
	SelectLanguage string `json:"select_language" validate:"required"`
	Codec          string `json:"codec" validate:"required"`
	Bitrate        string `json:"bitrate" validate:"required"`
}
