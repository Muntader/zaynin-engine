package queue

import (
	"encoding/json"
	"testing"

	"github.com/muntader/zaynin-engine/internal/vod/types"
)

func TestNewTaskDownloadUsesGeneralQueue(t *testing.T) {
	payload := StartWorkflowPayload{
		Config: types.Config{JobID: "job-1"},
	}

	task, err := NewTask(TypeDownloadSource, payload, QueueNameGeneral)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if task.Type() != TypeDownloadSource {
		t.Fatalf("task type = %q, want %q", task.Type(), TypeDownloadSource)
	}

	var decoded StartWorkflowPayload
	if err := json.Unmarshal(task.Payload(), &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.Config.JobID != "job-1" {
		t.Fatalf("job_id = %q, want job-1", decoded.Config.JobID)
	}
}

func TestNewTaskEncodeUsesCPUQueue(t *testing.T) {
	payload := WorkflowStatePayload{WorkspacePath: "/tmp/ws"}

	task, err := NewTask(TypeRunEncoding, payload, QueueNameEncoder)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if task.Type() != TypeRunEncoding {
		t.Fatalf("task type = %q, want %q", task.Type(), TypeRunEncoding)
	}

	var decoded WorkflowStatePayload
	if err := json.Unmarshal(task.Payload(), &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.WorkspacePath != "/tmp/ws" {
		t.Fatalf("workspace = %q, want /tmp/ws", decoded.WorkspacePath)
	}
}

func TestNewTaskCleanupPayloadRoundTrip(t *testing.T) {
	payload := CleanupPayload{WorkspacePath: "/tmp/job-42"}

	task, err := NewTask(TypeCleanupWorkspace, payload, QueueNameGeneral)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}

	var decoded CleanupPayload
	if err := json.Unmarshal(task.Payload(), &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.WorkspacePath != "/tmp/job-42" {
		t.Fatalf("workspace = %q, want /tmp/job-42", decoded.WorkspacePath)
	}
}
