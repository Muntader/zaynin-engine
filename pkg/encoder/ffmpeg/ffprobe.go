package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/muntader/zaynin-engine/pkg/encoder/types"
	"github.com/muntader/zaynin-engine/pkg/toolpath"
)

// GetMediaInfo runs ffprobe and parses stream/format JSON.
func GetMediaInfo(ctx context.Context, path string) (*types.FfprobeOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	}

	ffprobePath, err := toolpath.Resolve("ffprobe")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w\noutput: %s", err, string(output))
	}

	var data types.FfprobeOutput
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe JSON output: %w", err)
	}

	return &data, nil
}

// GetHDRFrameMetadata peeks at first video frame side data for HDR signals.
func GetHDRFrameMetadata(ctx context.Context, path string) (*types.HDRFrameInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-select_streams", "v:0",
		"-show_frames",
		"-read_intervals", "%+1",
		"-show_entries", "frame=side_data_list",
		path,
	}

	ffprobePath, err := toolpath.Resolve("ffprobe")
	if err != nil {
		return &types.HDRFrameInfo{}, err
	}
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &types.HDRFrameInfo{}, fmt.Errorf("ffprobe failed: %w", err)
	}

	var frameData types.FfprobeFrameOutput
	if err := json.Unmarshal(output, &frameData); err != nil {
		return &types.HDRFrameInfo{}, fmt.Errorf("JSON unmarshal failed: %w", err)
	}

	info := &types.HDRFrameInfo{}
	if len(frameData.Frames) > 0 && len(frameData.Frames[0].SideDataList) > 0 {
		for _, sideData := range frameData.Frames[0].SideDataList {
			if sideData.SideDataType == "Mastering display metadata" {
				info.HasMasteringDisplay = true
			}
			if sideData.SideDataType == "Content light level metadata" {
				info.HasContentLightLevel = true
				// max_content sometimes comes back as int, sometimes string   shrug
				maxCLLStr := fmt.Sprintf("%v", sideData.MaxContent)
				if maxCLLStr != "" {
					if cll, err := strconv.Atoi(strings.TrimSpace(maxCLLStr)); err == nil && cll > 0 {
						info.MaxCLL = cll
					} else {
						log.Printf("Failed to parse MaxCLL '%s': %v", maxCLLStr, err)
					}
				}
			}
		}
	}

	return info, nil
}
