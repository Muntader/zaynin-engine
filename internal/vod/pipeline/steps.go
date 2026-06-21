package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/muntader/zaynin-engine/internal/common/notifier"
	"github.com/muntader/zaynin-engine/internal/vod/service"
	zayninengine "github.com/muntader/zaynin-engine/pkg/encoder"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/audio"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/gif"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/packager"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/thumbnail"
	"github.com/muntader/zaynin-engine/pkg/encoder/media/video"
	zayninengineTypes "github.com/muntader/zaynin-engine/pkg/encoder/types"
	"golang.org/x/sync/errgroup"

	"github.com/muntader/zaynin-engine/internal/vod/types"
)

// JobContext is the scratch pad for one VOD job   paths, analysis, deps.
type JobContext struct {
	context.Context
	Config         types.Config
	AnalysisConfig zayninengineTypes.AnalysisConfig
	AnalysisReport *zayninengineTypes.AnalysisReport
	WorkspacePath  string
	SourcePath     string
	StorageService *service.StorageService
	Notifier       *notifier.Notifier
}

func prepareWorkspace(config types.Config, basePath string) (string, error) {
	workspacePath := filepath.Join(basePath, config.JobID)
	if err := os.MkdirAll(workspacePath, 0755); err != nil {
		return "", err
	}

	// snapshot config beside the mezzanine so we can debug jobs later
	configPath := filepath.Join(workspacePath, "config.json")
	configBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(configPath, configBytes, 0644); err != nil {
		return "", err
	}

	return workspacePath, nil
}

func downloadSource(j *JobContext) (string, error) {
	slog.Info("Delegating download to StorageService")
	sourceFile := filepath.Join(j.WorkspacePath, "source.mp4")

	err := j.StorageService.DownloadFile(j.Context, j.Config.InputStorage, sourceFile)
	if err != nil {
		return "", err
	}
	return sourceFile, nil
}

func analyzeMedia(j *JobContext) error {
	report, err := zayninengine.Analyze(j.SourcePath, j.WorkspacePath, &j.AnalysisConfig)
	if err != nil {
		return fmt.Errorf("analysis failed: %w", err)
	}

	fmt.Println(report)
	j.AnalysisReport = report
	return nil
}

func runEncoding(j *JobContext) error {
	if j.AnalysisReport == nil {
		return fmt.Errorf("cannot run encoding: analysis report is missing")
	}

	maxVideo := 1 // TODO: wire from job config when we expose it
	maxAudio := 3

	parentG, ctx := errgroup.WithContext(j.Context)

	parentG.Go(func() error {
		videoG, _ := errgroup.WithContext(ctx)
		videoG.SetLimit(maxVideo)

		for _, videoTask := range j.AnalysisReport.Video.Renditions {
			task := videoTask
			videoG.Go(func() error {
				libTask := zayninengine.VideoTask{Task: video.Task{
					InputFile:   task.InputFile,
					OutputFile:  task.OutputFile,
					HWAccel:     task.HWAccel,
					Codec:       task.Codec,
					RateControl: task.RateControl,
					HDR:         task.HDR,
					GOP:         task.GOP,
					Params:      task.Params,
					VideoFilter: task.VideoFilter,
					NoAudio:     task.NoAudio,
					Width:       task.Width,
					Height:      task.Height,
					Threads:     task.Threads,
				}}

				return zayninengine.EncodeVideo(ctx, libTask)
			})
		}

		return videoG.Wait()
	})

	// audio can run alongside video   different ffmpeg processes anyway
	parentG.Go(func() error {
		audioG, _ := errgroup.WithContext(ctx)
		audioG.SetLimit(maxAudio)

		for _, audioTask := range j.AnalysisReport.Audio {
			task := audioTask
			audioG.Go(func() error {
				libTask := zayninengine.AudioTask{Task: audio.Task{
					Name:           task.Name,
					InputFile:      task.InputFile,
					OutputFile:     task.OutputFile,
					SourceIndex:    task.SourceIndex,
					Language:       task.Language,
					Label:          task.Label,
					Codec:          task.Codec,
					Bitrate:        task.Bitrate,
					SampleRate:     task.SampleRate,
					Channels:       task.Channels,
					SourceChannels: task.SourceChannels,
					Description:    task.Description,
					IsDefault:      task.IsDefault,
					Processor:      task.Processor,
				}}
				return zayninengine.EncodeAudio(ctx, libTask)
			})
		}

		return audioG.Wait()
	})

	if err := parentG.Wait(); err != nil {
		return fmt.Errorf("encoding failed: %w", err)
	}

	// subtitles are cheap and ordering matters for packaging
	if len(j.AnalysisReport.Subtitles) > 0 {
		_, err := zayninengine.ProcessSubtitles(ctx, j.AnalysisReport.Subtitles)
		if err != nil {
			return fmt.Errorf("subtitle processing failed: %w", err)
		}
	}

	return nil
}

func fragmentAndPackage(j *JobContext) error {
	slog.Info("Starting in-memory fragmentation and packaging")

	fragOpts := zayninengine.FragmentOptions{
		InputDir:        j.WorkspacePath,
		SegmentDuration: j.Config.Outputs.StreamingPackage.Packaging.SegmentDurationSeconds,
	}
	if err := zayninengine.Fragment(fragOpts); err != nil {
		return fmt.Errorf("fragmentation failed: %w", err)
	}

	outputDir := filepath.Join(j.WorkspacePath, "outputs")
	if err := os.RemoveAll(outputDir); err != nil {
		slog.Warn("Could not clean outputs directory, packaging may fail", "error", err)
	}

	pkgOpts := zayninengine.PackageOptions{
		InputDir:  j.WorkspacePath,
		OutputDir: outputDir,
		PackagingConfig: zayninengine.PackagingConfig{
			SegmentDurationSeconds: j.Config.Outputs.StreamingPackage.Packaging.SegmentDurationSeconds,
			Formats:                j.Config.Outputs.StreamingPackage.Packaging.Formats,
			HLSSettings: packager.HLSSettings{
				Container: j.Config.Outputs.StreamingPackage.Packaging.HLSSettings.Container,
			},
		},
	}
	if err := zayninengine.Package(j.Context, j.AnalysisReport, pkgOpts); err != nil {
		return fmt.Errorf("packaging failed: %w", err)
	}

	return nil
}

func generateThumbnails(j *JobContext, thumbCfg types.Thumbnail) error {
	if !thumbCfg.Enable {
		return nil
	}
	outputDir := filepath.Join(j.WorkspacePath, "thumbnails", thumbCfg.OutputSubdir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("could not create thumbnail subdir %s: %w", outputDir, err)
	}

	task := zayninengine.ThumbnailTask{
		Task: thumbnail.Task{
			InputFile:       j.SourcePath,
			OutputDir:       outputDir,
			Mode:            thumbCfg.Mode,
			ImageFormat:     thumbCfg.ImageFormat,
			FilenamePattern: thumbCfg.FilenamePattern,
			Quality:         thumbCfg.Quality,
			IntervalSeconds: float64(thumbCfg.IntervalSeconds),
			Dimensions:      thumbnail.Dimensions(thumbCfg.Dimensions),
			Timestamps:      thumbCfg.Timestamps,
		},
	}

	_, err := zayninengine.GenerateThumbnail(j.Context, task)
	return err
}

func generateGIF(j *JobContext, gifCfg types.AnimatedGIF) error {
	if !gifCfg.Enable {
		return nil
	}
	outputDir := filepath.Join(j.WorkspacePath, "gifs", gifCfg.OutputSubdir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("could not create GIF subdir %s: %w", outputDir, err)
	}

	task := zayninengine.GIFTask{
		Task: gif.Task{
			InputFile:      j.SourcePath,
			OutputDir:      outputDir,
			OutputFilename: gifCfg.OutputFilename,
			StartTime:      float64(gifCfg.TimeRange.StartSeconds),
			Duration:       float64(gifCfg.TimeRange.DurationSeconds),
			FrameRate:      gifCfg.FrameRate,
			Dimensions:     gif.Dimensions(gifCfg.Dimensions),
		},
	}

	// soft-fail is decided in the handler; here we want a real error to bubble up
	_, err := zayninengine.GenerateGIF(j.Context, task, false)
	return err
}

func uploadOutput(j *JobContext) error {
	outputsDir := filepath.Join(j.WorkspacePath, "outputs")

	if err := os.MkdirAll(outputsDir, 0755); err != nil {
		return fmt.Errorf("failed to create outputs directory %s: %w", outputsDir, err)
	}

	// tuck sidecar assets under outputs/ so upload is one tree
	dirsToMove := []string{"gifs", "thumbnails", "clips"}
	for _, dirName := range dirsToMove {
		srcPath := filepath.Join(j.WorkspacePath, dirName)
		destPath := filepath.Join(outputsDir, dirName)

		if _, err := os.Stat(srcPath); err == nil {
			if err := os.Rename(srcPath, destPath); err != nil {
				return fmt.Errorf("failed to move directory from %s to %s: %w", srcPath, destPath, err)
			}
		} else if !os.IsNotExist(err) {
			slog.Error("Could not stat directory for moving", "path", srcPath, "error", err)
		}
	}

	if j.Config.DeliveryOptions != nil && j.Config.DeliveryOptions.KeepSourceVideo {
		sourceVideoPath := j.SourcePath
		if sourceVideoPath == "" {
			slog.Warn("KeepSourceVideo is true, but j.SourceVideoPath is empty. Cannot move source file.")
		} else if _, err := os.Stat(sourceVideoPath); !os.IsNotExist(err) {
			fileExt := filepath.Ext(sourceVideoPath)
			destFilename := j.Config.DeliveryOptions.KeepSourceName + fileExt
			destPath := filepath.Join(outputsDir, destFilename)

			if err := os.Rename(sourceVideoPath, destPath); err != nil {
				return fmt.Errorf("failed to move source video to outputs: %w", err)
			}
		} else {
			slog.Warn("KeepSourceVideo is true, but source file was not found", "path", sourceVideoPath)
		}
	}

	if _, err := os.Stat(outputsDir); os.IsNotExist(err) {
		slog.Warn("Output directory is empty or does not exist, skipping upload.", "path", outputsDir)
		return nil
	}

	return j.StorageService.UploadDirectory(j.Context, j.Config.OutputStorage, outputsDir)
}

func cleanupWorkspace(j *JobContext) error {
	return os.RemoveAll(j.WorkspacePath)
}
