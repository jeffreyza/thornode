package p2p

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
)

// ReadStreamWithBufferLimit checks the declared length before allocating or
// reading the payload. Read directly: a temporary buffered reader can read
// ahead into a rejected body or discard bytes belonging to the next frame.
// The exchange owner must reset the stream on cancellation or rejection.
func ReadStreamWithBufferLimit(stream network.Stream, maxPayload uint32) ([]byte, error) {
	return ReadStreamWithBufferLimitAndDeadline(stream, maxPayload, time.Now().Add(TimeoutReadPayload))
}

// ReadStreamWithBufferLimitAndDeadline uses the caller's absolute deadline
// instead of the ordinary frame timeout. It is for replies that depend on peer
// processing. The exchange owner still resets the stream on cancellation.
func ReadStreamWithBufferLimitAndDeadline(stream network.Stream, maxPayload uint32, deadline time.Time) ([]byte, error) {
	if stream == nil || maxPayload == 0 || maxPayload > MaxPayload || deadline.IsZero() {
		return nil, fmt.Errorf("invalid stream, payload limit %d, or deadline", maxPayload)
	}
	if ApplyDeadline.Load() {
		if err := stream.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
	}
	var header [LengthHeader]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		return nil, fmt.Errorf("read frame length: %w", err)
	}
	length := binary.LittleEndian.Uint32(header[:])
	if length > maxPayload {
		return nil, fmt.Errorf("payload length %d exceeds protocol maximum %d", length, maxPayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(stream, payload); err != nil {
		return nil, fmt.Errorf("read frame payload: %w", err)
	}
	return payload, nil
}
