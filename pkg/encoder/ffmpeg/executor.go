package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"

	"github.com/muntader/zaynin-engine/pkg/toolpath"
)

// Run ffmpeg with the given args. Logs command, attaches stderr on failure.
func Run(ctx context.Context, args []string) error {
	ffmpegPath, err := toolpath.Resolve("ffmpeg")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	log.Printf("Executing FFmpeg command: %v", cmd.String())

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg command failed: %w\nstderr:\n%s", err, stderr.String())
	}

	log.Println("FFmpeg command completed successfully.")
	return nil
}
