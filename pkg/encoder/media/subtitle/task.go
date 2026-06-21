package subtitle

import (
	"fmt"
)

type Task struct {
	InputFile    string
	OutputFile   string
	SourceIndex  int
	Language     string
	Label        string
	IsImageBased bool
	Action       string
}

// ToArgs   map one subtitle stream and force webvtt out.
func (t *Task) ToArgs() []string {
	return []string{
		"-y",
		"-i", t.InputFile,
		"-map", fmt.Sprintf("0:%d", t.SourceIndex),
		"-c:s", "webvtt",
		t.OutputFile,
	}
}
