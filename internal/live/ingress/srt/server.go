package srt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/media"

	srt "github.com/datarhei/gosrt"
)

// Source wraps an srt connection as ingress.Source.
type Source struct {
	id         string
	packetChan chan *media.Packet
	closeOnce  sync.Once
	stopChan   chan struct{}
	properties map[string]interface{}
}

func (s *Source) ID() string { return s.id }
func (s *Source) ReadPacket() (*media.Packet, error) {
	select {
	case p, ok := <-s.packetChan:
		if !ok {
			return nil, io.EOF
		}
		return p, nil
	case <-s.stopChan:
		return nil, io.EOF
	}
}
func (s *Source) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopChan)
		for range s.packetChan {
		}
	})
	return nil
}
func (s *Source) Properties() map[string]interface{} {
	return s.properties
}

// Server accepts srt callers and pumps mpegts into the pipeline.
type Server struct {
	config         configTypes.Config
	listener       srt.Listener
	manager        *core.Manager
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

func NewSRTServer(cfg configTypes.Config, manager *core.Manager) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		config:         cfg,
		manager:        manager,
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}, nil
}

// ListenAndServe starts the SRT server and blocks until it stops.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf(":%d", s.config.Server.Media.SRTPort)

	srtConfig := srt.DefaultConfig()
	srtConfig.StreamId = ""
	srtConfig.TSBPDMode = true
	srtConfig.TooLatePacketDrop = true
	srtConfig.NAKReport = true
	// 120ms is a sane default for live; tune if your proxy differs
	srtConfig.Latency = 120
	srtConfig.PeerLatency = 120
	srtConfig.LossMaxTTL = 40

	listener, err := srt.Listen("srt", addr, srtConfig)
	if err != nil {
		return fmt.Errorf("failed to listen on srt port %d: %w", s.config.Server.Media.SRTPort, err)
	}
	s.listener = listener
	slog.Info("Starting SRT server", "address", addr)

	for {
		select {
		case <-s.shutdownCtx.Done():
			slog.Info("SRT server accept loop shutting down.")
			return nil
		default:
			// Accept2 plays nicer with shutdown than blocking Accept
			req, err := listener.Accept2()
			if err != nil {
				if s.isGracefulShutdownError(err) {
					return nil
				}
				slog.Error("SRT accept failed", "error", err)
				continue
			}

			go s.handleConnection(req)
		}
	}
}

func (s *Server) handleConnection(req srt.ConnRequest) {
	streamID := req.StreamId()

	// todo: real auth before Accept()
	conn, err := req.Accept()
	if err != nil {
		slog.Error("SRT failed to accept connection", "streamId", streamID, "error", err)
		return
	}
	slog.Info("SRT connection established", "streamId", streamID)

	probePacketChan := make(chan *media.Packet, 500)
	source := &Source{
		id:         streamID,
		packetChan: make(chan *media.Packet, 1024),
		stopChan:   make(chan struct{}),
		properties: map[string]interface{}{
			"protocol":    "srt",
			"remote_addr": conn.RemoteAddr().String(),
		},
	}

	stream, err := s.manager.StartStream(source)
	if err != nil {
		slog.Error("Manager rejected SRT stream start", "streamId", streamID, "error", err)
		conn.Close()
		return
	}

	go s.probeStreamViaPipe(stream, probePacketChan)

	go s.pumpPackets(conn, source, stream.ID(), probePacketChan)
}

func (s *Server) probeStreamViaPipe(stream *core.Stream, probeChan <-chan *media.Packet) {
	defer stream.Properties().SignalReady() // always unblock supervisor, even on ffprobe fail

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "info",
		"-f", "mpegts",
		"-probesize", "2000000",
		"-analyzeduration", "10000000",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-",
	)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		slog.Error("Failed to create stdin pipe for ffprobe", "streamId", stream.ID(), "error", err)
		return
	}

	// ffprobe blocks if stderr/stdout fill up
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		slog.Error("Failed to start ffprobe command", "streamId", stream.ID(), "error", err)
		return
	}

	var wg sync.WaitGroup
	var probeDataSize int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			stdinPipe.Close() // eof tells ffprobe we're done
		}()

		var buffer bytes.Buffer
		const flushSize = 8192

		for packet := range probeChan {
			buffer.Write(packet.Data)
			probeDataSize += int64(len(packet.Data))

			if buffer.Len() >= flushSize {
				if _, err := buffer.WriteTo(stdinPipe); err != nil {
					slog.Warn("Error writing to ffprobe stdin", "streamId", stream.ID(), "error", err)
					return
				}
			}
		}

		if buffer.Len() > 0 {
			if _, err := buffer.WriteTo(stdinPipe); err != nil {
				slog.Warn("Error writing final data to ffprobe stdin", "streamId", stream.ID(), "error", err)
			}
		}

	}()

	err = cmd.Wait()

	wg.Wait()
	stderrOutput := stderrBuf.String()
	stdoutOutput := stdoutBuf.Bytes()

	slog.Info("FFprobe analysis completed",
		"streamId", stream.ID(),
		"exitError", err,
		"stdoutSize", len(stdoutOutput),
		"stderrSize", len(stderrOutput))

	if err != nil {
		slog.Error("ffprobe failed to analyze stream",
			"streamId", stream.ID(),
			"error", err,
			"stderr", stderrOutput)
		return
	}

	if stderrOutput != "" {
		slog.Info("ffprobe stderr output", "streamId", stream.ID(), "stderr", stderrOutput)
	}

	if len(stdoutOutput) == 0 {
		slog.Error("ffprobe returned empty output", "streamId", stream.ID())
		return
	}

	var jsonTest interface{}
	if err := json.Unmarshal(stdoutOutput, &jsonTest); err != nil {
		slog.Error("ffprobe returned invalid JSON", "streamId", stream.ID(), "error", err, "output", string(stdoutOutput))
		return
	}

	slog.Info("ffprobe returned valid JSON", "streamId", stream.ID(), "outputPreview", string(stdoutOutput[:min(200, len(stdoutOutput))]))

	if err := stream.Properties().UpdateFromFFprobeOutput(stdoutOutput); err != nil {
		slog.Error("Failed to update stream properties from ffprobe output", "streamId", stream.ID(), "error", err)
	} else {
		slog.Info("Successfully updated stream properties from ffprobe", "streamId", stream.ID())
	}
}

func (s *Server) pumpPackets(conn srt.Conn, source *Source, streamID string, probeChan chan<- *media.Packet) {
	defer func() {
		slog.Info("Closing SRT connection and deactivating stream", "streamId", streamID)
		conn.Close()
		s.manager.DeactivateStream(streamID)

		// close probe channel so ffprobe goroutine can finish
		if probeChan != nil {
			close(probeChan)
		}
	}()

	buffer := make([]byte, 8192)
	packetsSentToProbe := 0
	const maxProbePackets = 1000
	var totalBytesReceived int64

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if err != io.EOF && !s.isGracefulShutdownError(err) {
				slog.Warn("SRT read error", "streamId", streamID, "error", err)
			}
			break
		}

		if n > 0 {
			totalBytesReceived += int64(n)

			conn.SetReadDeadline(time.Now().Add(2 * time.Second))

			packetData := make([]byte, n)
			copy(packetData, buffer[:n])

			packet := &media.Packet{
				Type: media.PacketTypeMPEGTS,
				Data: packetData,
			}

			// tee a copy to ffprobe until we have enough for analysis
			if probeChan != nil {
				select {
				case probeChan <- packet:
					packetsSentToProbe++
					if packetsSentToProbe >= maxProbePackets {
						slog.Info("Sent enough packets to probe, closing probe channel",
							"streamId", streamID,
							"packetsSent", packetsSentToProbe,
							"totalBytes", totalBytesReceived)
						close(probeChan)
						probeChan = nil
					}
				default:
					// probe full   give up on analysis, main path still works
					slog.Warn("Probe channel blocked, closing it", "streamId", streamID)
					close(probeChan)
					probeChan = nil
				}
			}

			// Always send the packet to the main source channel for the transcoder.
			select {
			case source.packetChan <- packet:
				// Packet sent successfully
			case <-source.stopChan:
				return
			default:
				// main channel full   shouldnt happen often with this buffer size
				//slog.Warn("Main packet channel full, dropping packet", "streamId", streamID)
			}
		}
	}

	slog.Info("Packet pump finished", "streamId", streamID, "totalBytesReceived", totalBytesReceived)
}

func (s *Server) isGracefulShutdownError(err error) bool {
	// listener close shows up as a generic net error from gosrt
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "read" || opErr.Op == "accept"
	}
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection")
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.listener == nil {
		slog.Warn("SRT Server shutdown called, but it was not actively listening.")
		return
	}
	slog.Info("Shutting down SRT server...")
	s.shutdownCancel()
	s.listener.Close()
	slog.Info("SRT server shutdown complete.")
}

func (s *Source) Format() string {
	return "mpegts"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
