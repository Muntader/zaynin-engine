package core

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/muntader/zaynin-engine/internal/live/egress"
	"github.com/muntader/zaynin-engine/internal/live/ingress"
	"github.com/muntader/zaynin-engine/internal/live/media"
)

// Stream fans out packets from one source to many sinks.
type Stream struct {
	id         string
	source     ingress.Source
	properties *StreamProperties
	config     *media.StreamConfig
	startTime  time.Time

	sinksMutex sync.RWMutex
	sinks      map[string]egress.Sink

	stopOnce sync.Once
	stopChan chan struct{}
	runWg    sync.WaitGroup

	// late joiners need the first headers or they cant decode anything
	cacheMutex        sync.RWMutex
	cachedMetadata    *media.Packet
	cachedAudioConfig *media.Packet
	cachedVideoConfig *media.Packet
}

func NewStream(source ingress.Source, config *media.StreamConfig) *Stream {
	return &Stream{
		id:         source.ID(),
		source:     source,
		config:     config,
		properties: NewStreamProperties(),
		startTime:  time.Now(),
		sinks:      make(map[string]egress.Sink),
		stopChan:   make(chan struct{}),
	}
}

func (s *Stream) ID() string {
	return s.id
}

func (s *Stream) Properties() *StreamProperties {
	return s.properties
}

func (s *Stream) Config() *media.StreamConfig {
	return s.config
}

func (s *Stream) Source() ingress.Source {
	return s.source
}

func (s *Stream) GetAllSinks() []egress.Sink {
	s.sinksMutex.RLock()
	defer s.sinksMutex.RUnlock()
	sinkList := make([]egress.Sink, 0, len(s.sinks))
	for _, sink := range s.sinks {
		sinkList = append(sinkList, sink)
	}
	return sinkList
}

func (s *Stream) WaitUntilStopped() {
	<-s.stopChan
}

func (s *Stream) Run() {
	s.runWg.Add(1)
	defer s.runWg.Done()

	for {
		// bail out fast if Stop() already fired
		select {
		case <-s.stopChan:
			return
		default:
		}

		packet, err := s.source.ReadPacket()
		if err != nil {
			if err != io.EOF {
				slog.Error("Error reading packet from source", "streamID", s.id, "error", err)
			} else {
				//slog.Info("Source has ended (EOF). Exiting stream loop.", "streamID", s.id)
			}
			// source gone   supervisor will notice and clean up
			return
		}

		s.cacheAndBroadcast(packet)
	}
}

func (s *Stream) Stop() {
	s.stopOnce.Do(func() {

		close(s.stopChan) // unblocks WaitUntilStopped

		s.runWg.Wait() // dont close sinks while Run is still reading

		// unblock ReadPacket if its stuck waiting
		if s.source != nil {
			_ = s.source.Close()
		}
		s.closeAllSinks()
	})
}

// AddSink wires a new consumer and catches it up on headers.
func (s *Stream) AddSink(sink egress.Sink) {
	s.sinksMutex.Lock()
	s.sinks[sink.ID()] = sink
	s.sinksMutex.Unlock()

	s.replayCacheFor(sink)
}

func (s *Stream) RemoveSink(sinkID string) {
	s.sinksMutex.Lock()
	sink, ok := s.sinks[sinkID]
	if ok {
		delete(s.sinks, sinkID)
	}
	s.sinksMutex.Unlock()

	if ok {
		if err := sink.Close(); err != nil {
			slog.Warn("Error closing removed sink", "streamID", s.id, "sinkID", sinkID, "error", err)
		}
		slog.Info("Removed sink", "streamID", s.id, "sinkID", sinkID)
	}
}

func (s *Stream) cacheAndBroadcast(packet *media.Packet) {
	s.cachePacket(packet)

	s.sinksMutex.RLock()
	defer s.sinksMutex.RUnlock()

	for _, sink := range s.sinks {
		if err := sink.WritePacket(packet); err != nil {
			// slow sink shows up here first   worth watching in prod
			slog.Warn("Error writing packet to sink", "streamID", s.id, "sinkID", sink.ID(), "error", err)
		}
	}
}

func (s *Stream) cachePacket(packet *media.Packet) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	switch {
	case packet.Type == media.PacketTypeMetadata && s.cachedMetadata == nil:
		s.cachedMetadata = packet
	case packet.Type == media.PacketTypeAudio && packet.IsSequenceHeader && s.cachedAudioConfig == nil:
		s.cachedAudioConfig = packet
	case packet.Type == media.PacketTypeVideo && packet.IsSequenceHeader && s.cachedVideoConfig == nil:
		s.cachedVideoConfig = packet
	}
}

func (s *Stream) replayCacheFor(sink egress.Sink) {
	s.cacheMutex.RLock()
	defer s.cacheMutex.RUnlock()

	slog.Debug("Replaying cached headers to new sink", "streamID", s.id, "sinkID", sink.ID())

	if s.cachedMetadata != nil {
		_ = sink.WritePacket(s.cachedMetadata)
	}
	if s.cachedAudioConfig != nil {
		_ = sink.WritePacket(s.cachedAudioConfig)
	}
	if s.cachedVideoConfig != nil {
		_ = sink.WritePacket(s.cachedVideoConfig)
	}
}

func (s *Stream) closeAllSinks() {
	s.sinksMutex.Lock()
	defer s.sinksMutex.Unlock()

	fmt.Println("Closing all sinks for stream:")
	for id, sink := range s.sinks {
		if err := sink.Close(); err != nil {
			slog.Warn("Error closing sink during stream shutdown", "streamID", s.id, "sinkID", id, "error", err)
		}
	}
	s.sinks = make(map[string]egress.Sink)
}
