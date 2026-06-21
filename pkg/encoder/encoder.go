// Programmatic API for analyze, encode, and package media   same stuff the old CLI did.
package zayninengine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/muntader/zaynin-engine/pkg/encoder/ffmpeg"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/analyzer"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/audio"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/gif"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/packager"
	drm "github.com/muntader/zaynin-engine/pkg/encoder/media/packager/drm"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/subtitle"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/thumbnail"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/video"
	"github.com/muntader/zaynin-engine/pkg/encoder/types"
	"github.com/muntader/zaynin-engine/pkg/toolpath"
)

// VideoTask wraps the internal video task   same fields as the old JSON config.
type VideoTask struct {
	video.Task
}

// AudioTask wraps the internal audio task.
type AudioTask struct {
	audio.Task
}

// One subtitle job to run.
type SubtitleJob struct {
	InputFile    string `json:"input_file"`
	OutputFile   string `json:"output_file"`
	SourceIndex  int    `json:"source_index"`
	Language     string `json:"language"`
	Label        string `json:"label"`
	IsImageBased bool   `json:"is_image_based"`
	Action       string `json:"action"` // "convert_to_vtt" etc
}

// FragmentOptions for mp4fragment.
type FragmentOptions struct {
	InputDir        string
	SegmentDuration int // segment length in seconds
}

// AudioMetadata gets written into audio JSON during fragmentation.
type AudioMetadata struct {
	Language        string `json:"language"`
	Label           string `json:"label"`
	SourceIndex     int    `json:"source_index"`
	SourceIsDefault bool   `json:"source_is_default"`
	SourceIsForced  bool   `json:"source_is_forced"`
	Description     string `json:"description"`
	IsDefault       bool   `json:"is_default"`
	InputFile       string `json:"input_file"`
	OutputFile      string `json:"output_file"`
	Codec           string `json:"codec"`
	Bitrate         string `json:"bitrate"`
	Channels        int    `json:"channels"`
	SourceChannels  int    `json:"source_channels"`
}

// PackageOptions for Bento4 packaging.
type PackageOptions struct {
	InputDir        string
	OutputDir       string
	PackagingConfig PackagingConfig
}

type PackagingConfig struct {
	SegmentDurationSeconds int                  `json:"segment_duration_seconds"`
	Formats                []string             `json:"formats"` // "cmaf", "hls", ...
	HLSSettings            packager.HLSSettings `json:"hls_settings"`
	Drm                    DrmConfig            `json:"drm"`
}

// DrmConfig lines up with the drm block in job JSON.
type DrmConfig struct {
	Enable    bool         `json:"enable"`
	ContentID string       `json:"content_id"`
	Provider  ProviderInfo `json:"provider"`
	Dash      DashConfig   `json:"dash"`
	Hls       HlsConfig    `json:"hls"`
}

type ProviderInfo struct {
	Type   string          `json:"type"`   // "axinom", "simple_aes", ...
	Config json.RawMessage `json:"config"` // provider-specific blob
}

// DRM systems per output format.
type DashConfig struct {
	Systems []string `json:"systems"`
}
type HlsConfig struct {
	Systems []string `json:"systems"`
}

type ThumbnailTask struct {
	thumbnail.Task
}

type GIFTask struct {
	gif.Task
}

// Analyze runs ffprobe + planning. nil jobConfig → auto ABR ladder.
func Analyze(inputFile, outputDir string, jobConfig *types.AnalysisConfig) (*types.AnalysisReport, error) {
	if jobConfig == nil {
		log.Println("No custom config provided. Generating job using automatic profile.")
	}

	// make paths absolute so downstream tools don't get confused
	absInputFile, err := filepath.Abs(inputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for input file: %w", err)
	}

	absOutputDir, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for output directory: %w", err)
	}

	if err := os.MkdirAll(absOutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory %s: %w", absOutputDir, err)
	}

	slugBaseName := slugify(inputFile)

	log.Printf("Analyzing '%s', all output will be based in: %s", absInputFile, absOutputDir)

	report, err := analyzer.Run(absInputFile, absOutputDir, slugBaseName, jobConfig)
	if err != nil {
		return nil, fmt.Errorf("analysis and task generation failed: %w", err)
	}

	log.Println("Analysis complete. An AnalysisReport has been returned.")
	return report, nil
}

// EncodeVideo runs ffmpeg for one video task (single or multi-pass).
func EncodeVideo(ctx context.Context, task VideoTask) error {
	// mkdir output dir first
	outputDir := filepath.Dir(task.OutputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory '%s': %w", outputDir, err)
	}

	log.Printf("Starting video encode for output: %s", task.OutputFile)
	if err := task.Validate(); err != nil {
		return fmt.Errorf("invalid video task configuration: %w", err)
	}
	return runEncodingWorkflow(ctx, &task.Task)
}

// EncodeAudio extracts/encodes one audio track.
func EncodeAudio(ctx context.Context, task AudioTask) error {

	// mkdir output dir first
	outputDir := filepath.Dir(task.OutputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory '%s': %w", outputDir, err)
	}

	log.Printf("Starting audio extraction for output: %s", task.OutputFile)
	ffmpegArgs := task.ToArgs()

	// 10 min should be plenty for audio
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := ffmpeg.Run(ctx, ffmpegArgs); err != nil {
		return fmt.Errorf("failed to process audio task for %s: %w", task.OutputFile, err)
	}

	log.Printf("Successfully created: %s", task.OutputFile)
	return nil
}

// ProcessSubtitles converts subtitle streams to WebVTT in parallel.
// Returns paths for jobs that succeeded (partial success possible).
func ProcessSubtitles(ctx context.Context, jobs []subtitle.Task) ([]string, error) {
	var tasks []subtitle.Task
	for _, job := range jobs {

		outputDir := filepath.Dir(job.OutputFile)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			log.Printf("WARN: Failed to create output directory '%s' for job, skipping: %v", outputDir, err)
			continue
		}

		tasks = append(tasks, subtitle.Task{
			InputFile:   job.InputFile,
			OutputFile:  job.OutputFile,
			SourceIndex: job.SourceIndex,
		})
	}

	if len(tasks) == 0 {
		log.Println("No 'convert_to_vtt' subtitle tasks to process.")
		return []string{}, nil
	}

	log.Printf("Found %d 'convert_to_vtt' task(s). Starting parallel execution.", len(tasks))
	resultsChan := make(chan string, len(tasks))
	errChan := make(chan error, len(tasks))
	var wg sync.WaitGroup

	for _, task := range tasks {
		wg.Add(1)
		go func(t subtitle.Task) {
			defer wg.Done()
			log.Printf("Starting subtitle job for: %s", t.OutputFile)
			args := t.ToArgs()
			taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			if err := ffmpeg.Run(taskCtx, args); err != nil {
				errChan <- fmt.Errorf("failed to process task for %s: %w", t.OutputFile, err)
				return
			}
			resultsChan <- t.OutputFile
		}(task)
	}

	wg.Wait()
	close(errChan)
	close(resultsChan)

	var successfulFiles []string
	for path := range resultsChan {
		successfulFiles = append(successfulFiles, path)
	}

	var allErrors []string
	for err := range errChan {
		allErrors = append(allErrors, err.Error())
	}

	if len(allErrors) > 0 {
		return successfulFiles, fmt.Errorf("one or more subtitle tasks failed: %s", strings.Join(allErrors, "; "))
	}

	log.Println("All subtitle tasks completed successfully.")
	return successfulFiles, nil
}

// Fragment turns plain MP4/M4A into fMP4 via Bento4 mp4fragment   needed before packaging.
func Fragment(opts FragmentOptions) error {
	mp4fragmentPath, err := toolpath.Resolve("mp4fragment")
	if err != nil {
		return err
	}
	durationMS := opts.SegmentDuration * 1000

	log.Println("Starting fragmentation process...")
	log.Printf("Bento4 Tool:      %s", mp4fragmentPath)
	log.Printf("Segment Duration: %d seconds (%d ms)", opts.SegmentDuration, durationMS)

	videoDir := filepath.Join(opts.InputDir, "video")
	fragmentedVideoDir := filepath.Join(opts.InputDir, "fragmented")
	audioDir := filepath.Join(opts.InputDir, "audio")
	fragmentedAudioDir := filepath.Join(opts.InputDir, "fragmented_audio")

	// video
	if err := processMediaDirectory(mp4fragmentPath, videoDir, fragmentedVideoDir, ".mp4", "video", durationMS); err != nil {
		return fmt.Errorf("failed to fragment video files: %w", err)
	}

	// audio
	if err := processMediaDirectory(mp4fragmentPath, audioDir, fragmentedAudioDir, ".m4a", "audio", durationMS); err != nil {
		return fmt.Errorf("failed to fragment audio files: %w", err)
	}

	log.Println("--- Fragmentation complete! ---")
	return nil
}

// Package builds DASH/HLS (and friends) from fragmented media, optional DRM.
func Package(ctx context.Context, analysisReport *types.AnalysisReport, opts PackageOptions) error {
	log.Println("Starting media packaging process...")

	var fetchedKeys *drm.DRMKeys
	var err error
	if opts.PackagingConfig.Drm.Enable {
		fetchedKeys, err = processDrmConfig(opts.PackagingConfig.Drm)
		if err != nil {
			return fmt.Errorf("DRM/Encryption processing failed: %w", err)
		}
		log.Println("Successfully prepared keys for DRM/Encryption.")
	} else {
		log.Println("DRM/Encryption is disabled in the configuration.")
	}

	baseOptions, err := buildBasePackagerOptions(opts.InputDir, analysisReport, &opts.PackagingConfig, fetchedKeys)
	if err != nil {
		return fmt.Errorf("failed to prepare packaging options: %w", err)
	}

	mp4dashPath, err := toolpath.Resolve("mp4dash")
	if err != nil {
		return err
	}
	mp4hlsPath, err := toolpath.Resolve("mp4hls")
	if err != nil {
		return err
	}
	bentoPackager := packager.NewBento4Packager(mp4dashPath, mp4hlsPath)

	formatsToProcess := opts.PackagingConfig.Formats
	if len(formatsToProcess) == 0 {
		formatsToProcess = []string{"cmaf"} // default when nothing specified
	}

	for _, format := range formatsToProcess {
		log.Printf("\n===== Processing format: %s =====\n", format)
		mediaOptions := *baseOptions
		mediaOptions.Format = format
		mediaOptions.OutputDir = opts.OutputDir

		packageCmd, err := bentoPackager.Package(ctx, mediaOptions)
		if err != nil {
			return fmt.Errorf("failed to build package command for format %s: %w", format, err)
		}

		log.Println("Executing Bento4 command:", strings.Join(packageCmd.Args, " "))
		packageCmd.Stdout = os.Stdout
		packageCmd.Stderr = os.Stderr
		if err := packageCmd.Run(); err != nil {
			return fmt.Errorf("packaging for format '%s' failed: %w", format, err)
		}
	}

	log.Println("--- All packaging complete! ---")
	return nil
}

// GenerateThumbnail   VTT sprite, BIF, or single images depending on task mode.
func GenerateThumbnail(ctx context.Context, task ThumbnailTask) (*thumbnail.GenerationResult, error) {
	if err := os.MkdirAll(task.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory '%s': %w", task.OutputDir, err)
	}

	log.Printf("Starting thumbnail generation for input: %s", task.InputFile)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	result, err := thumbnail.Generate(ctx, task.Task)
	if err != nil {
		return nil, fmt.Errorf("thumbnail generation failed: %w", err)
	}

	log.Println("Thumbnail generation completed successfully.")
	return result, nil
}

// GenerateGIF two-pass palette GIF from a video clip.
func GenerateGIF(ctx context.Context, task GIFTask, allowSoftFail bool) (*gif.GenerationResult, error) {
	if err := os.MkdirAll(task.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory '%s': %w", task.OutputDir, err)
	}
	log.Printf("Starting GIF generation...")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := gif.Generate(ctx, task.Task)
	if err != nil {
		if allowSoftFail {
			log.Printf("WARNING: GIF generation failed but is set to soft-fail. Error: %v", err)
			return &gif.GenerationResult{}, nil // soft fail → empty result, no error
		}
		return nil, fmt.Errorf("GIF generation failed: %w", err)
	}

	log.Println("GIF generation completed successfully.")
	return result, nil
}

// processMediaDirectory fragments everything in a subdir (video or audio).
func processMediaDirectory(mp4fragmentPath, inputSubDir, outputSubDir, fileExt, mediaType string, durationMS int) error {
	if _, err := os.Stat(inputSubDir); os.IsNotExist(err) {
		log.Printf("Info: '%s' subfolder not found, skipping %s fragmentation.", mediaType, mediaType)
		return nil
	}
	if err := os.MkdirAll(outputSubDir, 0755); err != nil {
		return fmt.Errorf("could not create output directory '%s': %w", outputSubDir, err)
	}

	log.Printf("--- Starting %s fragmentation: %s -> %s ---", mediaType, inputSubDir, outputSubDir)
	mediaFiles, err := packager.FindMediaFiles(inputSubDir, fileExt)
	if err != nil || len(mediaFiles) == 0 {
		log.Printf("Info: No %s files found in '%s' to fragment.", fileExt, inputSubDir)
		return nil
	}

	for _, inputFile := range mediaFiles {
		baseName := filepath.Base(inputFile)
		outputFile := filepath.Join(outputSubDir, baseName)
		log.Printf("Fragmenting '%s'", baseName)
		cmd := exec.Command(mp4fragmentPath, "--fragment-duration", fmt.Sprintf("%d", durationMS), inputFile, outputFile)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("fragmentation failed for '%s': %w", baseName, err)
		}
	}
	return nil
}

// runEncodingWorkflow   single or multi-pass depending on rate control config.
func runEncodingWorkflow(ctx context.Context, task *video.Task) error {
	rc := task.RateControl
	totalPasses := 1
	isConstrainedQuality := (rc.QP > 0 || rc.CRF > 0) && rc.VBR != nil

	if !isConstrainedQuality {
		if rc.VBR != nil && rc.VBR.Passes > 1 {
			totalPasses = rc.VBR.Passes
		} else if rc.CBR != nil && rc.CBR.Passes > 1 {
			totalPasses = rc.CBR.Passes
		}
	}

	log.Printf("Starting %d-pass encode...", totalPasses)
	baseArgs := task.ToArgs()
	for pass := 1; pass <= totalPasses; pass++ {
		if totalPasses > 1 {
			log.Printf("--- Starting Pass %d of %d ---", pass, totalPasses)
		}

		passArgs := make([]string, len(baseArgs))
		copy(passArgs, baseArgs)

		if pass < totalPasses {
			passArgs = append(passArgs, "-pass", fmt.Sprintf("%d", pass), "-f", "null", os.DevNull)
		} else {
			if totalPasses > 1 {
				passArgs = append(passArgs, "-pass", fmt.Sprintf("%d", pass))
			}
			passArgs = append(passArgs, task.OutputFile)
		}

		passCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		err := ffmpeg.Run(passCtx, passArgs)
		cancel()

		if err != nil {
			return fmt.Errorf("pass %d failed: %w", pass, err)
		}
	}
	log.Println("--- Encode completed successfully. ---")
	return nil
}

// processDrmConfig fetches or builds DRM keys from packaging config.
func processDrmConfig(cfg DrmConfig) (*drm.DRMKeys, error) {
	if cfg.Provider.Type != "simple_aes" && cfg.ContentID == "" {
		return nil, fmt.Errorf("drm.content_id is required for provider '%s'", cfg.Provider.Type)
	}

	providerJSON, err := json.Marshal(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to re-marshal provider configuration: %w", err)
	}

	keyService, err := drm.NewKeyServiceProvider(providerJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to create DRM/Encryption key service: %w", err)
	}

	if cfg.Provider.Type == "simple_aes" {
		return keyService.FetchKeys(context.Background(), "", nil)
	}

	systemsSet := make(map[string]struct{})
	for _, sys := range cfg.Dash.Systems {
		systemsSet[strings.ToUpper(sys)] = struct{}{}
	}
	for _, sys := range cfg.Hls.Systems {
		systemsSet[strings.ToUpper(sys)] = struct{}{}
	}
	var allSystems []string
	for sys := range systemsSet {
		allSystems = append(allSystems, sys)
	}

	log.Printf("Requesting DRM keys for systems: %v", allSystems)
	return keyService.FetchKeys(context.Background(), cfg.ContentID, allSystems)
}

// buildBasePackagerOptions finds fragmented files and wires up packager options.
func buildBasePackagerOptions(inputDir string, analysisReport *types.AnalysisReport, cfg *PackagingConfig, keys *drm.DRMKeys) (*packager.Options, error) {
	fragmentedDir := filepath.Join(inputDir, "fragmented")
	audioDir := filepath.Join(inputDir, "fragmented_audio")
	subtitleDir := filepath.Join(inputDir, "subtitle")

	videoFiles, err := packager.FindMediaFiles(fragmentedDir, ".mp4")
	if err != nil || len(videoFiles) == 0 {
		return nil, fmt.Errorf("no .mp4 files found in %s: %w", fragmentedDir, err)
	}

	// map analysis audio tasks → packager track info
	audioTracks := make([]packager.AudioTrackInfo, len(analysisReport.Audio))
	for i, task := range analysisReport.Audio {
		audioTracks[i] = packager.AudioTrackInfo{
			Language:   task.Language,
			Label:      task.Label,
			OutputFile: filepath.Join(audioDir, filepath.Base(task.OutputFile)),
			//AutoSelect: task.AutoSelect, // TODO: pick this up later
		}
	}

	// same for subtitles
	subtitleTracks := make([]packager.SubtitleTrackInfo, len(analysisReport.Subtitles))
	for i, task := range analysisReport.Subtitles {
		subtitleTracks[i] = packager.SubtitleTrackInfo{
			Language:   task.Language,
			Label:      task.Label,
			OutputFile: filepath.Join(subtitleDir, filepath.Base(task.OutputFile)),
			//AutoSelect: task.AutoSelect, // TODO: pick this up later
		}
	}

	return &packager.Options{
		VideoFiles:             videoFiles,
		AudioTracks:            audioTracks,
		SubtitleTracks:         subtitleTracks,
		DRM:                    keys,
		SegmentDurationSeconds: cfg.SegmentDurationSeconds,
		HLSSettings:            cfg.HLSSettings,
	}, nil
}

var (
	nonAlphanumericRegex     = regexp.MustCompile(`[^a-zA-Z0-9\p{L}]+`)
	multipleUnderscoresRegex = regexp.MustCompile(`_+`)
)

// slugify turns a filename into something safe for output paths.
func slugify(fullPath string) string {
	base := filepath.Base(fullPath)
	ext := filepath.Ext(base)
	slug := strings.TrimSuffix(base, ext)
	slug = nonAlphanumericRegex.ReplaceAllString(slug, "_")
	slug = multipleUnderscoresRegex.ReplaceAllString(slug, "_")
	slug = strings.Trim(slug, "_")
	return strings.ToLower(slug)
}
