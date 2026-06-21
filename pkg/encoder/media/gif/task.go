package gif

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/muntader/zaynin-engine/pkg/encoder/ffmpeg"
)

type Task struct {
	InputFile      string
	OutputDir      string
	StartTime      float64
	Duration       float64
	Dimensions     Dimensions
	FrameRate      int
	OutputFilename string
}

// 0 or -1 on one axis → derive from source aspect ratio.
type Dimensions struct {
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

type GenerationResult struct {
	OutputPath string `json:"output_path"`
}

// Generate   palette pass then gif pass, the usual ffmpeg recipe.
func Generate(ctx context.Context, task Task) (*GenerationResult, error) {
	log.Println("Starting animated GIF generation...")
	if err := validateTask(task); err != nil {
		return nil, fmt.Errorf("invalid GIF task: %w", err)
	}

	sourceDims, err := getVideoDimensions(ctx, task.InputFile)
	if err != nil {
		return nil, fmt.Errorf("could not get source video dimensions: %w", err)
	}

	targetW, targetH := calculateTargetDimensions(task.Dimensions, sourceDims)
	log.Printf("Target GIF dimensions calculated as: %dx%d", targetW, targetH)

	palettePath := filepath.Join(os.TempDir(), fmt.Sprintf("palette-%d.png", os.Getpid()))
	defer os.Remove(palettePath)

	log.Println("Pass 1/2: Generating color palette...")
	vfPalette := fmt.Sprintf("fps=%d,scale=%d:-1:flags=lanczos,palettegen", task.FrameRate, targetW)
	paletteArgs := []string{
		"-ss", fmt.Sprintf("%.4f", task.StartTime),
		"-t", fmt.Sprintf("%.4f", task.Duration),
		"-i", task.InputFile,
		"-vf", vfPalette,
		"-y", palettePath,
	}
	if err := ffmpeg.Run(ctx, paletteArgs); err != nil {
		return nil, fmt.Errorf("ffmpeg palette generation failed: %w", err)
	}

	log.Println("Pass 2/2: Generating final GIF...")
	outputFilename := task.OutputFilename
	if outputFilename == "" {
		outputFilename = "animated.gif"
	}
	finalOutputPath := filepath.Join(task.OutputDir, outputFilename)

	vfGIF := fmt.Sprintf("fps=%d,scale=%d:-1:flags=lanczos[x];[x][1:v]paletteuse", task.FrameRate, targetW)
	gifArgs := []string{
		"-ss", fmt.Sprintf("%.4f", task.StartTime),
		"-t", fmt.Sprintf("%.4f", task.Duration),
		"-i", task.InputFile,
		"-i", palettePath,
		"-filter_complex", vfGIF,
		"-y", finalOutputPath,
	}
	if err := ffmpeg.Run(ctx, gifArgs); err != nil {
		return nil, fmt.Errorf("ffmpeg GIF creation failed: %w", err)
	}

	log.Printf("Successfully generated GIF: %s", finalOutputPath)
	return &GenerationResult{OutputPath: finalOutputPath}, nil
}

func validateTask(task Task) error {
	if task.InputFile == "" {
		return fmt.Errorf("InputFile is required")
	}
	if task.OutputDir == "" {
		return fmt.Errorf("OutputDir is required")
	}
	if task.Duration <= 0 {
		return fmt.Errorf("duration_seconds must be positive")
	}
	if task.FrameRate <= 0 || task.FrameRate > 60 {
		return fmt.Errorf("frame_rate must be between 1 and 60")
	}
	if task.Dimensions.Width <= 0 && task.Dimensions.Height <= 0 {
		return fmt.Errorf("at least one dimension (width or height) must be specified")
	}
	return nil
}

func getVideoDimensions(ctx context.Context, inputFile string) (*Dimensions, error) {
	mediaInfo, err := ffmpeg.GetMediaInfo(ctx, inputFile)
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	for _, stream := range mediaInfo.Streams {
		if stream.CodecType == "video" {
			return &Dimensions{Width: stream.Width, Height: stream.Height}, nil
		}
	}
	return nil, fmt.Errorf("no video stream found")
}

func calculateTargetDimensions(taskDims Dimensions, sourceDims *Dimensions) (targetW, targetH int) {
	if taskDims.Width > 0 && taskDims.Height > 0 {
		return taskDims.Width, taskDims.Height
	}
	sourceAspect := float64(sourceDims.Width) / float64(sourceDims.Height)
	if taskDims.Width > 0 {
		targetW = taskDims.Width
		targetH = int(math.Round(float64(targetW) / sourceAspect))
		return
	}
	if taskDims.Height > 0 {
		targetH = taskDims.Height
		targetW = int(math.Round(float64(targetH) * sourceAspect))
		return
	}
	// shouldn't hit this if validateTask ran
	return sourceDims.Width, sourceDims.Height
}
