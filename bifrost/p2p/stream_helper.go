package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	LengthHeader        = 4 // LengthHeader represent how many bytes we used as header
	TimeoutReadPayload  = time.Second * 40
	TimeoutWritePayload = time.Second * 40
	MaxPayload          = 20000000 // 20M
	// TSSFramePreflightBytes bounds how much of a framed TSS message is read
	// before its type, session subscription, and authenticated peer are
	// authorized. WrappedMessage places message_type/message_id before payload.
	TSSFramePreflightBytes = 4096
)

// ErrTSSFramePreflightRejected distinguishes an authorization/size rejection
// from a transport read failure so callers can reset the hostile stream
// immediately instead of retaining it for normal session cleanup.
var ErrTSSFramePreflightRejected = errors.New("TSS frame preflight rejected")

// applyDeadline will be true , and only disable it when we are doing test
// the reason being the p2p network , mocknet, mock stream doesn't support SetReadDeadline ,SetWriteDeadline feature
var ApplyDeadline = &atomic.Bool{}

func init() {
	ApplyDeadline.Store(true)
}

type StreamMgr struct {
	unusedStreams map[string][]network.Stream
	streamLocker  *sync.RWMutex
	logger        zerolog.Logger
}

func NewStreamMgr() *StreamMgr {
	return &StreamMgr{
		unusedStreams: make(map[string][]network.Stream),
		streamLocker:  &sync.RWMutex{},
		logger:        log.With().Str("module", "communication").Logger(),
	}
}

func (sm *StreamMgr) ReleaseStream(msgID string) {
	sm.streamLocker.RLock()
	usedStreams, okStream := sm.unusedStreams[msgID]
	unknownStreams, okUnknown := sm.unusedStreams[StreamUnknown]
	sm.streamLocker.RUnlock()
	streams := append(usedStreams, unknownStreams...) // nolint:gocritic
	if okStream || okUnknown {
		for _, el := range streams {
			err := el.Reset()
			if err != nil {
				sm.logger.Error().Err(err).Msg("fail to reset the stream,skip it")
			}
		}
		sm.streamLocker.Lock()
		delete(sm.unusedStreams, msgID)
		delete(sm.unusedStreams, StreamUnknown)
		sm.streamLocker.Unlock()
	}
}

func (sm *StreamMgr) AddStream(msgID string, stream network.Stream) {
	if stream == nil {
		return
	}
	sm.streamLocker.Lock()
	defer sm.streamLocker.Unlock()
	entries, ok := sm.unusedStreams[msgID]
	if !ok {
		entries = []network.Stream{stream}
		sm.unusedStreams[msgID] = entries
	} else {
		entries = append(entries, stream)
		sm.unusedStreams[msgID] = entries
	}
}

// closeStreamAndDrain closes the local write side before returning, then
// observes the peer EOF in the background. AwaitEOF resets the stream on
// timeout or protocol error, so callers do not need to retain another owner.
func closeStreamAndDrain(stream network.Stream) error {
	if stream == nil {
		return nil
	}
	if err := stream.Close(); err != nil {
		_ = stream.Reset()
		return err
	}
	// The drain can outlive its caller. Own the logger value so a later
	// reconfiguration of the package-global logger cannot race with it.
	drainLogger := log.Logger
	go func(logger zerolog.Logger) {
		if err := helpers.AwaitEOF(stream); err != nil {
			logger.Debug().Err(err).Msg("fail to drain peer EOF after stream close")
		}
	}(drainLogger)
	return nil
}

// ReadStreamWithBuffer read data from the given stream
func ReadStreamWithBuffer(stream network.Stream) ([]byte, error) {
	if ApplyDeadline.Load() {
		if err := stream.SetReadDeadline(time.Now().Add(TimeoutReadPayload)); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return nil, errReset
			}
			return nil, err
		}
	}
	streamReader := bufio.NewReader(stream)
	lengthBytes := make([]byte, LengthHeader)
	n, err := io.ReadFull(streamReader, lengthBytes)
	if n != LengthHeader || err != nil {
		return nil, fmt.Errorf("error in read the message head: %w", err)
	}
	length := binary.LittleEndian.Uint32(lengthBytes)
	if length > MaxPayload {
		return nil, fmt.Errorf("payload length:%d exceed max payload length:%d", length, MaxPayload)
	}
	dataBuf := make([]byte, length)
	n, err = io.ReadFull(streamReader, dataBuf)
	if uint32(n) != length || err != nil {
		return nil, fmt.Errorf("short read err(%w), we would like to read: %d, however we only read: %d", err, length, n)
	}
	return dataBuf, nil
}

// readStreamWithBufferPreflight reads only a bounded prefix before invoking
// preflight. The full frame allocation occurs only after preflight authorizes
// the declared length and parsed session header.
func readStreamWithBufferPreflight(
	stream network.Stream,
	prefixLimit int,
	preflight func(length uint32, prefix []byte) error,
) ([]byte, error) {
	if stream == nil {
		return nil, fmt.Errorf("stream is nil")
	}
	if prefixLimit <= 0 {
		return nil, fmt.Errorf("invalid TSS frame preflight prefix limit")
	}
	if preflight == nil {
		return nil, fmt.Errorf("TSS frame preflight callback is nil")
	}
	if ApplyDeadline.Load() {
		if err := stream.SetReadDeadline(time.Now().Add(TimeoutReadPayload)); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return nil, errReset
			}
			return nil, err
		}
	}

	lengthBytes := make([]byte, LengthHeader)
	n, err := io.ReadFull(stream, lengthBytes)
	if n != LengthHeader || err != nil {
		return nil, fmt.Errorf("error in read the message head: %w", err)
	}
	length := binary.LittleEndian.Uint32(lengthBytes)
	if length > MaxPayload {
		// the global size gate runs before preflight(), so it must carry the same
		// sentinel: otherwise handleStreamTss parks the stream in StreamUnknown
		// instead of resetting it, and an oversized header from any gater-passing
		// peer leaks a stream with no ceremony to release it.
		return nil, fmt.Errorf("%w: payload length:%d exceed max payload length:%d", ErrTSSFramePreflightRejected, length, MaxPayload)
	}

	prefixBytes := int(length)
	if prefixBytes > prefixLimit {
		prefixBytes = prefixLimit
	}
	prefix := make([]byte, prefixBytes)
	n, err = io.ReadFull(stream, prefix)
	if n != prefixBytes || err != nil {
		return nil, fmt.Errorf("short TSS preflight read err(%w), wanted:%d read:%d", err, prefixBytes, n)
	}
	if preflightErr := preflight(length, prefix); preflightErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrTSSFramePreflightRejected, preflightErr)
	}

	dataBuf := make([]byte, int(length))
	copy(dataBuf, prefix)
	remaining := int(length) - prefixBytes
	if remaining == 0 {
		return dataBuf, nil
	}
	n, err = io.ReadFull(stream, dataBuf[prefixBytes:])
	if n != remaining || err != nil {
		return nil, fmt.Errorf("short read err(%w), we would like to read: %d, however we only read: %d", err, remaining, n)
	}
	return dataBuf, nil
}

// WriteStreamWithBuffer write the message to stream
func WriteStreamWithBuffer(msg []byte, stream network.Stream) error {
	return writeStreamWithBuffer(msg, stream, true)
}

// writeStreamWithBuffer lets a caller that owns terminal cleanup suppress the
// legacy deadline-error reset. All exported callers retain the old behavior.
func writeStreamWithBuffer(msg []byte, stream network.Stream, resetDeadlineError bool) error {
	length := uint32(len(msg))
	// Fail fast on the sender: receivers enforce the same bound and reject
	// the frame after the bandwidth is already spent, attributing the error
	// to the wrong side.
	if length > MaxPayload {
		return fmt.Errorf("payload length:%d exceed max payload length:%d", length, MaxPayload)
	}
	lengthBytes := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(lengthBytes, length)
	if ApplyDeadline.Load() {
		if err := stream.SetWriteDeadline(time.Now().Add(TimeoutWritePayload)); err != nil {
			if resetDeadlineError {
				if errReset := stream.Reset(); errReset != nil {
					return errReset
				}
			}
			return err
		}
	}
	streamWrite := bufio.NewWriter(stream)
	n, err := streamWrite.Write(lengthBytes)
	if n != LengthHeader || err != nil {
		return fmt.Errorf("fail to write head: %w", err)
	}
	n, err = streamWrite.Write(msg)
	if err != nil {
		return err
	}
	if uint32(n) != length {
		return fmt.Errorf("short write, we would like to write: %d, however we only write: %d", length, n)
	}
	err = streamWrite.Flush()
	if err != nil {
		return fmt.Errorf("fail to flush stream: %w", err)
	}
	return nil
}

// ReadStreamWithBufferWithContext reads data from the given stream with context awareness
func ReadStreamWithBufferWithContext(ctx context.Context, stream network.Stream) ([]byte, error) {
	// Determine deadline from context or use default
	deadline := time.Now().Add(TimeoutReadPayload)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	// Apply deadline to stream
	if ApplyDeadline.Load() {
		if err := stream.SetReadDeadline(deadline); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return nil, errReset
			}
			return nil, err
		}
	}

	// Buffered to guarantee the reader can always send even if the caller
	// has already returned on context cancellation.
	readDone := make(chan struct{}, 1)
	var dataBuf []byte
	var readErr error

	// Perform the read operation in a separate goroutine
	go func() {
		streamReader := bufio.NewReader(stream)
		lengthBytes := make([]byte, LengthHeader)
		n, err := io.ReadFull(streamReader, lengthBytes)
		if n != LengthHeader || err != nil {
			readErr = fmt.Errorf("error in read the message head: %w", err)
			readDone <- struct{}{}
			return
		}

		length := binary.LittleEndian.Uint32(lengthBytes)
		if length > MaxPayload {
			readErr = fmt.Errorf("payload length:%d exceed max payload length:%d", length, MaxPayload)
			readDone <- struct{}{}
			return
		}

		dataBuf = make([]byte, length)
		n, err = io.ReadFull(streamReader, dataBuf)
		if uint32(n) != length || err != nil {
			readErr = fmt.Errorf("short read err(%w), we would like to read: %d, however we only read: %d", err, length, n)
		}
		readDone <- struct{}{}
	}()

	// Wait for either read completion or context cancellation
	select {
	case <-readDone:
		return dataBuf, readErr
	case <-ctx.Done():
		// Context was canceled or timed out
		// Reset the stream to prevent resource leaks
		_ = stream.Reset()
		return nil, ctx.Err()
	}
}

// WriteStreamWithBufferWithContext writes the message to stream with context awareness
func WriteStreamWithBufferWithContext(ctx context.Context, msg []byte, stream network.Stream) error {
	if len(msg) > MaxPayload {
		return fmt.Errorf("payload length:%d exceed max payload length:%d", len(msg), MaxPayload)
	}
	// Determine deadline from context or use default
	deadline := time.Now().Add(TimeoutWritePayload)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}

	// Apply deadline to stream
	if ApplyDeadline.Load() {
		if err := stream.SetWriteDeadline(deadline); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return errReset
			}
			return err
		}
	}

	// Buffered to guarantee the writer can always send even if the caller
	// has already returned on context cancellation.
	writeDone := make(chan error, 1)

	// Perform the write operation in a separate goroutine
	go func() {
		streamWrite := bufio.NewWriter(stream)
		length := uint32(len(msg))
		lengthBytes := make([]byte, LengthHeader)
		binary.LittleEndian.PutUint32(lengthBytes, length)

		n, err := streamWrite.Write(lengthBytes)
		if n != LengthHeader || err != nil {
			writeDone <- fmt.Errorf("fail to write head: %w", err)
			return
		}

		n, err = streamWrite.Write(msg)
		if err != nil {
			writeDone <- err
			return
		}

		if uint32(n) != length {
			writeDone <- fmt.Errorf("short write, we would like to write: %d, however we only write: %d", length, n)
			return
		}

		err = streamWrite.Flush()
		if err != nil {
			writeDone <- fmt.Errorf("fail to flush stream: %w", err)
			return
		}

		writeDone <- nil
	}()

	// Wait for either write completion or context cancellation
	select {
	case err := <-writeDone:
		return err
	case <-ctx.Done():
		// Context was canceled or timed out
		// Reset the stream to prevent resource leaks
		_ = stream.Reset()
		return ctx.Err()
	}
}
