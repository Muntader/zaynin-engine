package ingress

import (
	"github.com/muntader/zaynin-engine/internal/live/media"
)

type Source interface {
	ID() string // rtmp key or srt stream id
	ReadPacket() (*media.Packet, error)
	Close() error
	Format() string
}
