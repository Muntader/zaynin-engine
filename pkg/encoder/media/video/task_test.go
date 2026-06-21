package video

import (
	"strings"
	"testing"
)

func TestTaskValidateRequiresCoreFields(t *testing.T) {
	task := &Task{}
	if err := task.Validate(); err == nil {
		t.Fatal("expected validation error for empty task")
	}
}

func TestTaskValidateAcceptsLibx264CRF(t *testing.T) {
	task := &Task{
		InputFile:  "/in.mp4",
		OutputFile: "/out.mp4",
		Codec:      "libx264",
		RateControl: RateControl{
			CRF: 23,
		},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestTaskToArgsIncludesPreset(t *testing.T) {
	task := &Task{
		InputFile:  "/in.mp4",
		OutputFile: "/out.mp4",
		Codec:      "libx264",
		RateControl: RateControl{
			CRF: 23,
		},
		Params: Params{
			Preset: "fast",
		},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	args := task.ToArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-preset") || !strings.Contains(joined, "fast") {
		t.Fatalf("expected preset in args, got: %v", args)
	}
}
