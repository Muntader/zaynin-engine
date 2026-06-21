package rtmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	manager3 "github.com/muntader/zaynin-engine/internal/live/core"

	"log/slog"

	"github.com/yutopp/go-rtmp"
)

// Server is the rtmp listener   one handler per connection.
type Server struct {
	config   configTypes.Config
	srv      *rtmp.Server
	listener net.Listener
	manager  *manager3.Manager
}

func NewRTMPServer(cfg configTypes.Config, manager *manager3.Manager) (*Server, error) {
	s := &Server{
		config:  cfg,
		manager: manager,
	}

	rtmpSrv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			h := &Handler{
				manager:   s.manager,
				appConfig: &s.config,
			}

			return conn, &rtmp.ConnConfig{
				Handler:      h,
				ControlState: rtmp.StreamControlStateConfig{},
			}
		},
	})

	s.srv = rtmpSrv

	return s, nil
}

func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf(":%d", s.config.Server.Media.RTMPPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on tcp port %d: %w", s.config.Server.Media.RTMPPort, err)
	}

	s.listener = listener
	slog.Info("Starting RTMP server", "address", addr)

	// blocks until listener closes
	if err := s.srv.Serve(listener); err != nil && !s.isGracefulShutdownError(err) {
		return err
	}
	return nil
}

// isGracefulShutdownError   closing the listener mid-accept looks like an error but isnt.
func (s *Server) isGracefulShutdownError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// classic symptom of listener.Close() racing accept()
		return opErr.Op == "accept" && opErr.Err.Error() == "use of closed network connection"
	}
	return false
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.listener == nil {
		slog.Warn("RTMP Server shutdown called, but it was not actively listening.")
		return
	}
	// unblocks ListenAndServe
	if err := s.listener.Close(); err != nil {
		slog.Error("Error closing RTMP network listener", "error", err)
	}
}
