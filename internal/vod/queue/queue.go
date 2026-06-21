package queue

import (
	"runtime"

	"github.com/hibiken/asynq"
)

// NewCPUQueueServer runs ffmpeg-heavy tasks on the cpu queue.
func NewCPUQueueServer(redisAddr string, concurrency int) *asynq.Server {
	if concurrency < 1 {
		concurrency = runtime.NumCPU()
	}
	srv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr},
		asynq.Config{
			Concurrency: concurrency,
			Queues: map[string]int{
				QueueNameEncoder: 10,
			},
		},
	)
	return srv
}

// NewIOQueueServer runs download/upload tasks   mostly waiting on network/disk.
func NewIOQueueServer(redisAddr string) *asynq.Server {
	concurrency := 20 // tune if you're saturating disk or upstream bandwidth

	srv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr},
		asynq.Config{
			Concurrency: concurrency,
			Queues: map[string]int{
				QueueNameGeneral: 10,
			},
		},
	)
	return srv
}
