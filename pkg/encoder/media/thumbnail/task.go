package thumbnail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/muntader/zaynin-engine/pkg/encoder/ffmpeg"
)

type Task struct {
	InputFile string
	OutputDir string
	Mode      string // vtt_sprite, bif, single_image

	IntervalSeconds float64
	Timestamps      []float64 `json:"timestamps,omitempty"`

	Dimensions  Dimensions
	AspectMode  string // stretch (default), pad, crop
	ImageFormat string // jpg or webp
	Quality     int

	FilenamePattern  string
	ManifestFilename string `json:"manifest_filename,omitempty"`
	SpriteFilename   string `json:"sprite_filename,omitempty"`
}

// 0 on one axis → derive from source aspect
type Dimensions struct {
	Width  int
	Height int
}

type VideoDimensions struct {
	Width  int
	Height int
}

type GenerationResult struct {
	ManifestPath string   `json:"manifest_path,omitempty"`
	SpritePath   string   `json:"sprite_path,omitempty"`
	ImagePaths   []string `json:"image_paths,omitempty"`
}

func Generate(ctx context.Context, task Task) (*GenerationResult, error) {
	if err := validateTask(task); err != nil {
		return nil, fmt.Errorf("invalid task configuration: %w", err)
	}

	switch task.Mode {
	case "vtt_sprite":
		return generateVTTSprite(ctx, task)
	case "bif":
		return generateBIF(ctx, task)
	case "single_image":
		if len(task.Timestamps) > 0 {
			return generateSingleImagesByTimestamp(ctx, task)
		}
		return generateSingleImagesByInterval(ctx, task)
	default:
		return nil, fmt.Errorf("unsupported thumbnail mode: '%s'", task.Mode)
	}
}

func generateVTTSprite(ctx context.Context, task Task) (*GenerationResult, error) {
	tempDir, framePaths, err := extractFrames(ctx, task, "")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	numThumbs := len(framePaths)
	if numThumbs == 0 {
		return nil, fmt.Errorf("no frames were extracted")
	}

	log.Println("Stitching frames into a sprite sheet...")
	cols := int(math.Ceil(math.Sqrt(float64(numThumbs))))
	spriteImagePath, thumbWidth, thumbHeight, err := stitchSprite(ctx, task, framePaths, cols)
	if err != nil {
		return nil, fmt.Errorf("failed to stitch sprite: %w", err)
	}

	log.Println("Generating WebVTT manifest file...")
	manifestFilename := task.ManifestFilename
	if manifestFilename == "" {
		manifestFilename = "thumbnails.vtt"
	}
	vttPath := filepath.Join(task.OutputDir, manifestFilename)

	spriteImageFilename := filepath.Base(spriteImagePath)
	if err := generateVTTFile(vttPath, spriteImageFilename, numThumbs, cols, task.IntervalSeconds, thumbWidth, thumbHeight); err != nil {
		return nil, fmt.Errorf("failed to generate VTT file: %w", err)
	}

	return &GenerationResult{
		ManifestPath: vttPath,
		SpritePath:   spriteImagePath,
	}, nil
}

func generateBIF(ctx context.Context, task Task) (*GenerationResult, error) {
	tempDir, framePaths, err := extractFrames(ctx, task, "")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	numThumbs := len(framePaths)
	if numThumbs == 0 {
		return nil, fmt.Errorf("no frames were extracted")
	}

	log.Println("Generating BIF file...")
	manifestFilename := task.ManifestFilename
	if manifestFilename == "" {
		manifestFilename = "index.bif"
	}
	bifPath := filepath.Join(task.OutputDir, manifestFilename)

	bifFile, err := os.Create(bifPath)
	if err != nil {
		return nil, fmt.Errorf("could not create BIF file: %w", err)
	}
	defer bifFile.Close()

	// BIF index + concatenated jpegs
	magic := []byte{0x89, 0x42, 0x49, 0x46, 0x0d, 0x0a, 0x1a, 0x0a}
	version := uint32(0)
	imageCount := uint32(numThumbs)
	multiplier := uint32(1000)
	indexSize := uint64(20 + (12 * imageCount))
	var timestamps []uint32
	var offsets []uint64
	currentOffset := indexSize
	for i := 0; i < numThumbs; i++ {
		ts := uint32(float64(i) * task.IntervalSeconds * float64(multiplier))
		timestamps = append(timestamps, ts)
		offsets = append(offsets, currentOffset)
		fi, err := os.Stat(framePaths[i])
		if err != nil {
			return nil, fmt.Errorf("could not stat temp frame %s: %w", framePaths[i], err)
		}
		currentOffset += uint64(fi.Size())
	}
	buf := new(bytes.Buffer)
	buf.Write(magic)
	binary.Write(buf, binary.LittleEndian, version)
	binary.Write(buf, binary.LittleEndian, imageCount)
	binary.Write(buf, binary.LittleEndian, multiplier)
	binary.Write(buf, binary.LittleEndian, timestamps)
	binary.Write(buf, binary.LittleEndian, offsets)
	binary.Write(buf, binary.LittleEndian, uint64(currentOffset))
	if _, err := bifFile.Write(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("failed to write BIF index: %w", err)
	}
	for _, framePath := range framePaths {
		frameFile, err := os.Open(framePath)
		if err != nil {
			return nil, fmt.Errorf("could not open temp frame %s: %w", framePath, err)
		}
		if _, err := io.Copy(bifFile, frameFile); err != nil {
			frameFile.Close()
			return nil, fmt.Errorf("failed to copy frame data to BIF: %w", err)
		}
		frameFile.Close()
	}

	return &GenerationResult{ManifestPath: bifPath}, nil
}

func generateSingleImages(ctx context.Context, task Task) (*GenerationResult, error) {
	_, framePaths, err := extractFrames(ctx, task, task.FilenamePattern)
	if err != nil {
		return nil, err
	}
	return &GenerationResult{ImagePaths: framePaths}, nil
}

func extractFrames(ctx context.Context, task Task, filenamePattern string) (tempDir string, framePaths []string, err error) {
	var outputPathPattern string
	isTemp := false
	if filenamePattern == "" {
		isTemp = true
		tempDir, err = os.MkdirTemp("", "zayninengine-thumbs-")
		if err != nil {
			err = fmt.Errorf("failed to create temp directory: %w", err)
			return
		}
		outputPathPattern = filepath.Join(tempDir, fmt.Sprintf("thumb-%%04d.%s", task.ImageFormat))
	} else {
		if !strings.Contains(filenamePattern, "{index}") {
			err = fmt.Errorf("filenamePattern must contain '{index}' placeholder")
			return
		}
		basePattern := strings.Replace(filenamePattern, "{index}", "%04d", 1)
		outputPathPattern = filepath.Join(task.OutputDir, basePattern)
	}

	log.Printf("Step 1: Extracting frames with interval %.2fs...", task.IntervalSeconds)

	sourceVideoDims, err := getVideoDimensions(ctx, task.InputFile)
	if err != nil {
		err = fmt.Errorf("failed to get source video dimensions: %w", err)
		return
	}

	vfFilter, err := buildVFFilterString(task, sourceVideoDims)
	if err != nil {
		err = fmt.Errorf("could not build video filter: %w", err)
		return
	}

	extractArgs := []string{"-i", task.InputFile, "-vf", vfFilter, "-fps_mode", "vfr"}
	if task.ImageFormat == "jpg" {
		extractArgs = append(extractArgs, "-qscale:v", fmt.Sprintf("%d", task.Quality))
	} else if task.ImageFormat == "webp" {
		extractArgs = append(extractArgs, "-quality", fmt.Sprintf("%d", task.Quality))
	}

	extractArgs = append(extractArgs, "-pix_fmt", "yuv420p")
	extractArgs = append(extractArgs, outputPathPattern)

	if err = ffmpeg.Run(ctx, extractArgs); err != nil {
		err = fmt.Errorf("frame extraction failed: %w", err)
		return
	}

	globPattern := strings.Replace(outputPathPattern, "%04d", "*", 1)
	framePaths, err = filepath.Glob(globPattern)
	if err != nil {
		err = fmt.Errorf("could not find generated frames with glob '%s': %w", globPattern, err)
		return
	}
	if len(framePaths) == 0 {
		err = fmt.Errorf("no frames were generated by FFmpeg")
		return
	}

	if isTemp {
		return tempDir, framePaths, nil
	}
	return "", framePaths, nil
}

func generateSingleImagesByInterval(ctx context.Context, task Task) (*GenerationResult, error) {
	_, framePaths, err := extractFrames(ctx, task, task.FilenamePattern)
	if err != nil {
		return nil, err
	}
	return &GenerationResult{ImagePaths: framePaths}, nil
}

func generateSingleImagesByTimestamp(ctx context.Context, task Task) (*GenerationResult, error) {
	log.Printf("Starting extraction for %d specific timestamps.", len(task.Timestamps))

	sourceVideoDims, err := getVideoDimensions(ctx, task.InputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to get source video dimensions: %w", err)
	}

	// drop fps filter   we're seeking to explicit timestamps
	baseVfFilter, err := buildVFFilterString(task, sourceVideoDims)
	if err != nil {
		return nil, fmt.Errorf("could not build base video filter: %w", err)
	}
	vfParts := strings.Split(baseVfFilter, ",")
	simpleVfFilter := strings.Join(vfParts[1:], ",")

	var wg sync.WaitGroup
	errChan := make(chan error, len(task.Timestamps))
	pathChan := make(chan string, len(task.Timestamps))

	for i, ts := range task.Timestamps {
		wg.Add(1)
		go func(index int, timestamp float64) {
			defer wg.Done()

			outputPath, err := generateFilename(task, index, timestamp)
			if err != nil {
				errChan <- err
				return
			}

			args := []string{
				"-i", task.InputFile,
				"-ss", fmt.Sprintf("%.4f", timestamp), // accurate seek, bit slower
				"-vf", simpleVfFilter,
				"-frames:v", "1",
			}

			if task.ImageFormat == "jpg" {
				args = append(args, "-qscale:v", fmt.Sprintf("%d", task.Quality))
			} else {
				args = append(args, "-quality", fmt.Sprintf("%d", task.Quality))
			}

			args = append(args, "-pix_fmt", "yuv420p")

			args = append(args, outputPath)

			if err := ffmpeg.Run(ctx, args); err != nil {
				errChan <- fmt.Errorf("failed to extract frame at %.2fs: %w", timestamp, err)
				return
			}
			pathChan <- outputPath
		}(i, ts)
	}

	wg.Wait()
	close(errChan)
	close(pathChan)

	var allErrors []string
	for e := range errChan {
		allErrors = append(allErrors, e.Error())
	}
	if len(allErrors) > 0 {
		return nil, fmt.Errorf("one or more errors occurred during extraction:\n- %s", strings.Join(allErrors, "\n- "))
	}

	var generatedPaths []string
	for p := range pathChan {
		generatedPaths = append(generatedPaths, p)
	}

	return &GenerationResult{ImagePaths: generatedPaths}, nil
}

func getVideoDimensions(ctx context.Context, inputFile string) (*VideoDimensions, error) {
	mediaInfo, err := ffmpeg.GetMediaInfo(ctx, inputFile)
	if err != nil {
		return nil, fmt.Errorf("ffprobe execution failed for video %s: %w", inputFile, err)
	}
	if mediaInfo == nil || len(mediaInfo.Streams) == 0 {
		return nil, fmt.Errorf("no streams found in ffprobe output")
	}

	for _, stream := range mediaInfo.Streams {
		if stream.CodecType == "video" {
			if stream.Width <= 0 || stream.Height <= 0 {
				return nil, fmt.Errorf("invalid video dimensions: %dx%d", stream.Width, stream.Height)
			}
			return &VideoDimensions{
				Width:  stream.Width,
				Height: stream.Height,
			}, nil
		}
	}

	return nil, fmt.Errorf("no video stream found in input file")
}

func calculateTargetDimensions(task Task, sourceVideoDims *VideoDimensions) (targetW, targetH int, err error) {
	taskW := task.Dimensions.Width
	taskH := task.Dimensions.Height
	sourceW := sourceVideoDims.Width
	sourceH := sourceVideoDims.Height
	sourceAspect := float64(sourceW) / float64(sourceH)

	if taskW > 0 && taskH > 0 {
		return taskW, taskH, nil
	}

	if taskW > 0 && taskH <= 0 {
		targetH = int(math.Round(float64(taskW) / sourceAspect))
		return taskW, targetH, nil
	}

	if taskH > 0 && taskW <= 0 {
		targetW = int(math.Round(float64(taskH) * sourceAspect))
		return targetW, taskH, nil
	}

	return 0, 0, fmt.Errorf("at least one dimension (width or height) must be greater than zero")
}

func stitchSprite(ctx context.Context, task Task, framePaths []string, cols int) (spritePath string, thumbWidth int, thumbHeight int, err error) {
	if len(framePaths) == 0 {
		return "", 0, 0, fmt.Errorf("no frame paths provided to stitch")
	}

	w, h, err := probeImageDimensions(ctx, framePaths[0])
	if err != nil {
		return "", 0, 0, fmt.Errorf("could not probe frame dimensions: %w", err)
	}
	thumbWidth, thumbHeight = w, h

	spriteFilename := task.SpriteFilename
	if spriteFilename == "" {
		spriteFilename = fmt.Sprintf("sprite.%s", task.ImageFormat)
	}
	spritePath = filepath.Join(task.OutputDir, spriteFilename)

	rows := int(math.Ceil(float64(len(framePaths)) / float64(cols)))
	tileLayout := fmt.Sprintf("%dx%d", cols, rows)
	stitchArgs := []string{"-i", filepath.Join(filepath.Dir(framePaths[0]), fmt.Sprintf("thumb-%%04d.%s", task.ImageFormat)), "-filter_complex", fmt.Sprintf("tile=%s", tileLayout), "-an"}
	if task.ImageFormat == "jpg" {
		stitchArgs = append(stitchArgs, "-c:v", "mjpeg", "-qscale:v", fmt.Sprintf("%d", task.Quality))
	} else if task.ImageFormat == "webp" {
		stitchArgs = append(stitchArgs, "-c:v", "libwebp", "-quality", fmt.Sprintf("%d", task.Quality))
	}
	stitchArgs = append(stitchArgs, spritePath)

	if err := ffmpeg.Run(ctx, stitchArgs); err != nil {
		return "", 0, 0, err
	}
	return spritePath, thumbWidth, thumbHeight, nil
}

func buildVFFilterString(task Task, sourceVideoDims *VideoDimensions) (string, error) {
	var filters []string

	filters = append(filters, fmt.Sprintf("fps=1/%.2f", task.IntervalSeconds))
	targetW, targetH, err := calculateTargetDimensions(task, sourceVideoDims)
	if err != nil {
		return "", err
	}

	switch task.AspectMode {
	case "pad":
		sourceAspect := float64(sourceVideoDims.Width) / float64(sourceVideoDims.Height)
		targetAspect := float64(targetW) / float64(targetH)

		if math.Abs(sourceAspect-targetAspect) < 0.001 {
			filters = append(filters, fmt.Sprintf("scale=%d:%d", targetW, targetH))
		} else {
			var scaleW, scaleH int
			if sourceAspect > targetAspect {
				scaleW = targetW
				scaleH = int(math.Round(float64(targetW) / sourceAspect))
			} else {
				scaleH = targetH
				scaleW = int(math.Round(float64(targetH) * sourceAspect))
			}

			if scaleW > targetW {
				scaleW = targetW
				scaleH = int(math.Round(float64(targetW) / sourceAspect))
			}
			if scaleH > targetH {
				scaleH = targetH
				scaleW = int(math.Round(float64(targetH) * sourceAspect))
			}

			filters = append(filters, fmt.Sprintf("scale=%d:%d", scaleW, scaleH))
			filters = append(filters, fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", targetW, targetH))
		}

	case "crop":
		filters = append(filters, fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", targetW, targetH))
		filters = append(filters, fmt.Sprintf("crop=%d:%d", targetW, targetH))

	case "stretch", "":
		filters = append(filters, fmt.Sprintf("scale=%d:%d", targetW, targetH))

	default:
		return "", fmt.Errorf("unsupported aspect mode: '%s'. Supported modes are: stretch, pad, crop", task.AspectMode)
	}

	filterString := strings.Join(filters, ",")
	log.Printf("Generated video filter string: %s", filterString)
	return filterString, nil
}

func validateTask(task Task) error {
	if task.InputFile == "" {
		return fmt.Errorf("InputFile is required")
	}
	if task.OutputDir == "" {
		return fmt.Errorf("OutputDir is required")
	}

	if task.Dimensions.Width <= 0 && task.Dimensions.Height <= 0 {
		return fmt.Errorf("at least one dimension (width or height) must be greater than zero")
	}
	if task.Dimensions.Width < 0 || task.Dimensions.Height < 0 {
		return fmt.Errorf("dimensions cannot be negative, got width: %d, height: %d", task.Dimensions.Width, task.Dimensions.Height)
	}

	switch task.Mode {
	case "bif":
		if task.ImageFormat != "jpg" {
			return fmt.Errorf("BIF mode requires 'jpg' image format, got: '%s'", task.ImageFormat)
		}
	case "single_image":
		if len(task.Timestamps) == 0 && task.IntervalSeconds <= 0 {
			return fmt.Errorf("single_image mode requires either 'timestamps' array or a positive 'interval_seconds'")
		}
		if task.FilenamePattern == "" {
			return fmt.Errorf("single_image mode requires a FilenamePattern")
		}
		if !strings.Contains(task.FilenamePattern, "{index}") {
			return fmt.Errorf("FilenamePattern must contain '{index}' placeholder")
		}
	case "vtt_sprite":
		if task.IntervalSeconds <= 0 {
			return fmt.Errorf("IntervalSeconds must be greater than 0, got: %f", task.IntervalSeconds)
		}
	case "":
		return fmt.Errorf("Mode is required")
	default:
		return fmt.Errorf("unsupported mode: '%s'. Supported modes are: vtt_sprite, bif, single_image", task.Mode)
	}

	if task.ImageFormat != "jpg" && task.ImageFormat != "webp" {
		return fmt.Errorf("unsupported image format: '%s'. Supported formats are: jpg, webp", task.ImageFormat)
	}

	if task.Quality < 1 || task.Quality > 100 {
		return fmt.Errorf("Quality must be between 1 and 100, got: %d", task.Quality)
	}

	validAspectModes := []string{"stretch", "pad", "crop", ""}
	aspectModeValid := false
	for _, mode := range validAspectModes {
		if task.AspectMode == mode {
			aspectModeValid = true
			break
		}
	}
	if !aspectModeValid {
		return fmt.Errorf("unsupported aspect mode: '%s'. Supported modes are: stretch, pad, crop", task.AspectMode)
	}

	return nil
}

func generateVTTFile(vttPath, spriteFilename string, numThumbs, cols int, interval float64, thumbWidth, thumbHeight int) error {
	file, err := os.Create(vttPath)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	writer.WriteString("WEBVTT\n\n")
	for i := 0; i < numThumbs; i++ {
		startTime := float64(i) * interval
		endTime := float64(i+1) * interval
		row := i / cols
		col := i % cols
		x := col * thumbWidth
		y := row * thumbHeight
		timeStartStr := formatVTTTime(startTime)
		timeEndStr := formatVTTTime(endTime)
		fmt.Fprintf(writer, "%s --> %s\n", timeStartStr, timeEndStr)
		fmt.Fprintf(writer, "%s#xywh=%d,%d,%d,%d\n\n", spriteFilename, x, y, thumbWidth, thumbHeight)
	}
	return writer.Flush()
}

func probeImageDimensions(ctx context.Context, imagePath string) (width, height int, err error) {
	mediaInfo, err := ffmpeg.GetMediaInfo(ctx, imagePath)
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe execution failed for image %s: %w", imagePath, err)
	}
	if mediaInfo == nil || len(mediaInfo.Streams) == 0 {
		return 0, 0, fmt.Errorf("no streams found in ffprobe output")
	}
	stream := mediaInfo.Streams[0]
	if stream.CodecType != "video" {
		return 0, 0, fmt.Errorf("first stream is not a video stream")
	}
	return stream.Width, stream.Height, nil
}

func formatVTTTime(totalSeconds float64) string {
	h := int(totalSeconds / 3600)
	m := int(math.Mod(totalSeconds, 3600) / 60)
	s := int(math.Mod(totalSeconds, 60))
	ms := int((totalSeconds - float64(h*3600+m*60+s)) * 1000)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

func generateFilename(task Task, index int, timestamp float64) (string, error) {
	pattern := task.FilenamePattern
	pattern = strings.ReplaceAll(pattern, "{index}", fmt.Sprintf("%04d", index+1))
	pattern = strings.ReplaceAll(pattern, "{timestamp_s}", strconv.FormatInt(int64(math.Round(timestamp)), 10))
	pattern = strings.ReplaceAll(pattern, "{timestamp_ms}", strconv.FormatInt(int64(math.Round(timestamp*1000)), 10))
	return filepath.Join(task.OutputDir, pattern), nil
}
