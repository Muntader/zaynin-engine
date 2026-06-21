package media

import "time"

// PacketType so sinks can branch without knowing the wire format.
type PacketType uint8

const (
	PacketTypeAudio PacketType = iota
	PacketTypeVideo
	PacketTypeMetadata
	PacketTypeMPEGTS
)

// Packet is protocol-agnostic   rtmp and srt both normalize to this.
type Packet struct {
	Type             PacketType
	Timestamp        time.Duration
	Data             []byte
	IsKeyframe       bool
	IsSequenceHeader bool
}
