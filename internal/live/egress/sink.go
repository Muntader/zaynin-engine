package egress

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/muntader/zaynin-engine/internal/live/media"
)

type Sink interface {
	ID() string
	WritePacket(p *media.Packet) error
	Close() error
}

// SinkFactory keeps core from importing every sink implementation.
type SinkFactory func(config map[string]interface{}) (Sink, error)

var sinkRegistry = make(map[string]SinkFactory)

func RegisterSink(typeName string, factory SinkFactory) {
	sinkRegistry[typeName] = factory
}

// SupervisedSink restarts the underlying sink when writes start failing.
type SupervisedSink struct {
	mu         sync.RWMutex
	underlying Sink
	factory    SinkFactory
	config     map[string]interface{}

	restartCount int
	maxRestarts  int
	isStopped    bool
}

func NewSupervisedSink(typeName string, config map[string]interface{}) (Sink, error) {
	factory, ok := sinkRegistry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown sink type for supervision: %s", typeName)
	}

	initialSink, err := factory(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create initial sink for supervision: %w", err)
	}

	return &SupervisedSink{
		underlying:  initialSink,
		factory:     factory,
		config:      config,
		maxRestarts: 3,
	}, nil
}

func (s *SupervisedSink) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.underlying.ID()
}

// WritePacket swallows errors and triggers restart async   dont block the broadcast loop.
func (s *SupervisedSink) WritePacket(p *media.Packet) error {
	s.mu.RLock()
	if s.isStopped {
		s.mu.RUnlock()
		return nil
	}
	currentSink := s.underlying
	s.mu.RUnlock()

	err := currentSink.WritePacket(p)
	if err != nil {
		slog.Warn("Supervised sink detected a write error, triggering restart.", "id", s.ID(), "error", err)
		go s.restart()
	}
	return nil
}

func (s *SupervisedSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isStopped {
		return nil
	}
	s.isStopped = true

	return s.underlying.Close()
}

func (s *SupervisedSink) restart() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isStopped || s.restartCount >= s.maxRestarts {
		if !s.isStopped {
			slog.Error("Supervised sink: Maximum restart limit reached. Permanently stopping.", "id", s.ID())
			s.isStopped = true
		}
		return
	}

	s.restartCount++

	_ = s.underlying.Close()

	time.Sleep(time.Duration(s.restartCount*2) * time.Second)

	newSink, err := s.factory(s.config)
	if err != nil {
		slog.Error("Supervised sink: Failed to create new sink instance during restart.", "id", s.ID(), "error", err)
		return
	}

	s.underlying = newSink
}
