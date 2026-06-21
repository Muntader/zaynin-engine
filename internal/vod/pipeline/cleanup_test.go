package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/muntader/zaynin-engine/internal/vod/queue"
)

func TestHandleCleanupWorkspaceRemovesDirectory(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "outputs", "master.m3u8")
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(marker, []byte("#EXTM3U"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	payload, err := json.Marshal(queue.CleanupPayload{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	h := &HandlerContext{}
	task := asynq.NewTask(queue.TypeCleanupWorkspace, payload)
	if err := h.HandleCleanupWorkspace(context.Background(), task); err != nil {
		t.Fatalf("HandleCleanupWorkspace: %v", err)
	}

	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup: %v", err)
	}
}

func TestHandleCleanupWorkspaceMissingPathIsNoOp(t *testing.T) {
	payload, err := json.Marshal(queue.CleanupPayload{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	h := &HandlerContext{}
	task := asynq.NewTask(queue.TypeCleanupWorkspace, payload)
	if err := h.HandleCleanupWorkspace(context.Background(), task); err != nil {
		t.Fatalf("HandleCleanupWorkspace: %v", err)
	}
}

func TestHandleCleanupWorkspaceMissingDirectory(t *testing.T) {
	payload, err := json.Marshal(queue.CleanupPayload{WorkspacePath: filepath.Join(t.TempDir(), "already-gone")})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	h := &HandlerContext{}
	task := asynq.NewTask(queue.TypeCleanupWorkspace, payload)
	if err := h.HandleCleanupWorkspace(context.Background(), task); err != nil {
		t.Fatalf("expected missing directory to be ignored, got: %v", err)
	}
}
