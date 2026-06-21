package queue

import (
	"encoding/json"
	"github.com/hibiken/asynq"
)

const GroupVODOutputs = "vod-outputs"

const (
	TypeDownloadSource     = "vod:download"
	TypeAnalyzeMedia       = "vod:analyze"
	TypeRunEncoding        = "vod:encode"
	TypeFragmentAndPackage = "vod:package"
	TypeGenerateThumbnail  = "vod:artifact:thumbnail"
	TypeGenerateGIF        = "vod:artifact:gif"
	TypeUploadOutput       = "vod:upload"
	TypeCleanupWorkspace   = "vod:cleanup"
	TypeVodRecording       = "vod:recording"
)

const (
	QueueNameEncoder = "cpu"
	QueueNameGeneral = "general"
)

// NewTask marshals payload and pins the task to a queue with no retries.
func NewTask(taskType string, payload interface{}, queueName string) (*asynq.Task, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(taskType, p, asynq.Queue(queueName), asynq.MaxRetry(0)), nil
}
