package audio

import (
	"fmt"
	"strconv"
)

// Single audio encode/extract job.
type Task struct {
	Name           string
	InputFile      string
	OutputFile     string
	SourceIndex    int
	Language       string
	Label          string
	Codec          string
	Bitrate        string
	SampleRate     string
	Channels       int
	SourceChannels int
	Description    string
	IsDefault      bool
	Processor      string // cpu or gpu, mostly unused
}

// ToArgs builds ffmpeg argv. Does proper 5.1/7.1 → stereo downmix when needed.
func (t *Task) ToArgs() []string {
	args := []string{
		"-y",
		"-i", t.InputFile,
		"-vn",
		"-map", fmt.Sprintf("0:%d", t.SourceIndex),
	}

	if t.Codec != "" {
		args = append(args, "-c:a", t.Codec)
	} else {
		args = append(args, "-c:a", "aac")
	}

	if t.Codec != "copy" && t.Bitrate != "" {
		args = append(args, "-b:a", t.Bitrate)
	}

	isStereoDownmix := t.Channels == 2 && t.SourceChannels > 2
	var panFilter string

	// streaming-style downmix   same ballpark as netflix/prime
	if isStereoDownmix {
		switch t.SourceChannels {
		case 6: // 5.1 → stereo
			panFilter = "highpass=f=80,pan=stereo|c0<FL+0.65*FC+0.45*SL|c1<FR+0.65*FC+0.45*SR,compand=attacks=0.05:decays=0.2:points=-80/-80|-60/-40|-40/-20|-25/-15|-15/-10|-5/-5|0/0,loudnorm=I=-16:LRA=6:TP=-2.0"

		case 8: // 7.1 → stereo
			panFilter = "highpass=f=80,pan=stereo|c0<0.65*FL+0.55*FC+0.4*SL+0.15*SR+0.25*BL+0.1*BR+0.2*LFE|c1<0.65*FR+0.55*FC+0.4*SR+0.15*SL+0.25*BR+0.1*BL+0.2*LFE,compand=attacks=0.05:decays=0.2:points=-80/-80|-60/-40|-40/-20|-25/-15|-15/-10|-5/-5|0/0,loudnorm=I=-16:LRA=6:TP=-2.0"
		}
	}
	if panFilter != "" {
		args = append(args, "-af", panFilter)
	} else if t.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(t.Channels))
	}

	args = append(args, t.OutputFile)
	return args
}
