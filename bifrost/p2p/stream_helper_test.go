package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/magiconair/properties/assert"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

var testProtocolID protocol.ID = "/p2p/test-stream"

func TestPayloadDeadlineRelationship(t *testing.T) {
	if TimeoutReadPayload != 40*time.Second {
		t.Fatalf("TimeoutReadPayload = %s, want 40s", TimeoutReadPayload)
	}
	if TimeoutWritePayload != 40*time.Second {
		t.Fatalf("TimeoutWritePayload = %s, want 40s", TimeoutWritePayload)
	}
	if TimeoutReadPayload < TimeoutWritePayload {
		t.Fatalf("read timeout (%s) must be at least write timeout (%s)", TimeoutReadPayload, TimeoutWritePayload)
	}
}

type MockNetworkStream struct {
	*bytes.Buffer
	protocol            *protocol.ID
	errSetReadDeadLine  bool
	errSetWriteDeadLine bool
	errRead             bool
	id                  int64
}

func NewMockNetworkStream() *MockNetworkStream {
	return &MockNetworkStream{
		Buffer:   &bytes.Buffer{},
		protocol: &testProtocolID,
	}
}

func (m MockNetworkStream) Read(buf []byte) (int, error) {
	if m.errRead {
		return 0, errors.New("you asked for it")
	}
	return m.Buffer.Read(buf)
}

func (m MockNetworkStream) Close() error {
	return nil
}

func (m MockNetworkStream) Reset() error {
	return nil
}

func (m MockNetworkStream) SetDeadline(time.Time) error {
	return nil
}

func (m MockNetworkStream) SetReadDeadline(time.Time) error {
	if m.errSetReadDeadLine {
		return errors.New("you asked for it")
	}
	return nil
}

func (m MockNetworkStream) SetWriteDeadline(time.Time) error {
	if m.errSetWriteDeadLine {
		return errors.New("you asked for it")
	}
	return nil
}

func (m MockNetworkStream) Protocol() protocol.ID {
	return testProtocolID
}

func (m MockNetworkStream) SetProtocol(id protocol.ID) {}

func (s MockNetworkStream) ID() string {
	return strconv.FormatInt(s.id, 10)
}

func (m MockNetworkStream) Stat() network.Stat {
	return network.Stat{
		Direction: 0,
		Extra:     make(map[interface{}]interface{}),
	}
}

func (m MockNetworkStream) Conn() network.Conn {
	return nil
}

type gatedDrainErrorStream struct {
	*MockNetworkStream
	readStarted chan struct{}
	readGate    <-chan struct{}
}

func (s *gatedDrainErrorStream) Read([]byte) (int, error) {
	close(s.readStarted)
	<-s.readGate
	return 0, errors.New("forced drain error")
}

type drainLogWriter chan string

func (w drainLogWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestCloseStreamAndDrainOwnsLogger(t *testing.T) {
	originalLogger := log.Logger
	t.Cleanup(func() { log.Logger = originalLogger })

	ownedOutput := make(drainLogWriter, 1)
	log.Logger = zerolog.New(ownedOutput)

	readGate := make(chan struct{})
	stream := &gatedDrainErrorStream{
		MockNetworkStream: NewMockNetworkStream(),
		readStarted:       make(chan struct{}),
		readGate:          readGate,
	}
	if err := closeStreamAndDrain(stream); err != nil {
		t.Fatalf("close stream and drain: %v", err)
	}

	select {
	case <-stream.readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for background drain to start")
	}

	replacementOutput := make(drainLogWriter, 1)
	log.Logger = zerolog.New(replacementOutput)
	close(readGate)

	select {
	case output := <-ownedOutput:
		if !bytes.Contains([]byte(output), []byte("fail to drain peer EOF after stream close")) {
			t.Fatalf("owned logger output %q does not contain drain failure", output)
		}
	case <-replacementOutput:
		t.Fatal("background drain used the reconfigured global logger")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for background drain log")
	}
	select {
	case output := <-replacementOutput:
		t.Fatalf("reconfigured global logger unexpectedly received %q", output)
	default:
	}
}

func TestReadLength(t *testing.T) {
	testCases := []struct {
		name           string
		streamProvider func() network.Stream
		expectedLength uint32
		expectError    bool
		validator      func(t *testing.T)
	}{
		{
			name:           "happy path",
			expectedLength: 1024,
			expectError:    false,
			streamProvider: func() network.Stream {
				s := NewMockNetworkStream()
				buf := make([]byte, LengthHeader)
				binary.LittleEndian.PutUint32(buf, 1024)
				s.Buffer.Write(buf)
				s.Buffer.Write(bytes.Repeat([]byte("a"), 1024))
				return s
			},
		},
		{
			name:           "fail to set read dead line should return an error",
			expectedLength: 1024,
			expectError:    true,
			streamProvider: func() network.Stream {
				s := NewMockNetworkStream()
				s.errSetReadDeadLine = true
				buf := make([]byte, LengthHeader)
				binary.LittleEndian.PutUint32(buf, 1024)
				s.Buffer.Write(buf)
				s.Buffer.Write(bytes.Repeat([]byte("a"), 1024))
				return s
			},
		},
		{
			name:           "read exactly the given length of data",
			expectedLength: 1024,
			expectError:    false,
			streamProvider: func() network.Stream {
				s := NewMockNetworkStream()
				buf := make([]byte, LengthHeader)
				binary.LittleEndian.PutUint32(buf, 1024)
				s.Buffer.Write(buf)
				s.Buffer.Write(bytes.Repeat([]byte("a"), 1026))
				return s
			},
		},
		{
			name:           "fail to read should return an error",
			expectedLength: 1024,
			expectError:    true,
			streamProvider: func() network.Stream {
				s := NewMockNetworkStream()
				buf := make([]byte, LengthHeader)
				binary.LittleEndian.PutUint32(buf, 1024)
				s.Buffer.Write(buf)
				s.errRead = true
				return s
			},
		},
	}
	for _, tc := range testCases {
		ApplyDeadline.Store(true)
		t.Run(tc.name, func(st *testing.T) {
			stream := tc.streamProvider()
			l, err := ReadStreamWithBuffer(stream)
			if tc.expectError && err == nil {
				st.Errorf("expecting error , however got none")
				st.FailNow()
			}
			if !tc.expectError && err != nil {
				st.Error(err)
				st.FailNow()
			}
			if !tc.expectError && tc.expectedLength != uint32(len(l)) {
				st.Errorf("expecting length to be %d, however got :%d", tc.expectedLength, l)
				st.FailNow()
			}
		})
	}
}

func TestReadPayload(t *testing.T) {
	testCases := []struct {
		name           string
		streamProvider func() *MockNetworkStream
		expectedBytes  []byte
		expectError    bool
	}{
		{
			name: "happy path",
			streamProvider: func() *MockNetworkStream {
				stream := NewMockNetworkStream()
				input := []byte("hello world")
				err := WriteStreamWithBuffer(input, stream)
				if err != nil {
					t.Errorf("fail to write the data to stream")
					t.FailNow()
				}
				return stream
			},
			expectedBytes: []byte("hello world"),
			expectError:   false,
		},
	}
	for _, tc := range testCases {
		ApplyDeadline.Store(true)
		t.Run(tc.name, func(st *testing.T) {
			stream := tc.streamProvider()
			l, err := ReadStreamWithBuffer(stream)
			if err != nil {
				st.Errorf("fail to read length:%s", err)
				st.FailNow()
			}
			if tc.expectError && err == nil {
				st.Errorf("expecting error , however got none")
				st.FailNow()
			}
			if !tc.expectError && err != nil {
				st.Error(err)
				st.FailNow()
			}

			if !tc.expectError && !bytes.Equal(tc.expectedBytes, l) {
				st.Errorf("expecting %s, however got :%s", string(tc.expectedBytes), string(l))
				st.FailNow()
			}
		})
	}
}

func TestStreamManager(t *testing.T) {
	streamMgr := NewStreamMgr()
	stream := NewMockNetworkStream()

	streamMgr.AddStream("1", nil)
	assert.Equal(t, len(streamMgr.unusedStreams), 0)
	streamMgr.AddStream("1", stream)
	streamMgr.AddStream("2", stream)
	streamMgr.AddStream("3", stream)
	streamMgr.ReleaseStream("1")
	_, ok := streamMgr.unusedStreams["2"]
	assert.Equal(t, ok, true)
	_, ok = streamMgr.unusedStreams["3"]
	assert.Equal(t, ok, true)
	streamMgr.ReleaseStream("2")
	_, ok = streamMgr.unusedStreams["2"]
	assert.Equal(t, ok, false)
	streamMgr.ReleaseStream("3")
	assert.Equal(t, len(streamMgr.unusedStreams), 0)
	streamMgr.ReleaseStream("3")
	assert.Equal(t, len(streamMgr.unusedStreams), 0)
}

type limitTestStream struct {
	network.Stream
	reader *bytes.Reader
}

func (s *limitTestStream) Read(p []byte) (int, error)    { return s.reader.Read(p) }
func (*limitTestStream) SetReadDeadline(time.Time) error { return nil }

func limitTestFrame(n uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return b
}

func TestReadStreamWithBufferLimit(t *testing.T) {
	for _, limit := range []uint32{1, 5, 9, 10, MaxPayload} {
		payload := make([]byte, limit)
		input := append(limitTestFrame(limit), payload...)
		input = append(input, limitTestFrame(0)...)
		s := &limitTestStream{reader: bytes.NewReader(input)}
		result, err := ReadStreamWithBufferLimit(s, limit)
		require.NoError(t, err)
		require.Len(t, result, int(limit))
		result, err = ReadStreamWithBufferLimit(s, limit)
		require.NoError(t, err)
		require.Empty(t, result, "coalesced next frame must not be lost")
		s.reader = bytes.NewReader(append(limitTestFrame(limit+1), 1, 2, 3))
		_, err = ReadStreamWithBufferLimit(s, limit)
		require.ErrorContains(t, err, "exceeds protocol maximum")
		require.Equal(t, 3, s.reader.Len(), "oversized body must remain completely unread")
	}
	for _, input := range [][]byte{{1, 2}, append(limitTestFrame(3), 1)} {
		_, err := ReadStreamWithBufferLimit(&limitTestStream{reader: bytes.NewReader(input)}, 5)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	}
	_, err := ReadStreamWithBufferLimit(nil, 1)
	require.Error(t, err)
	for _, limit := range []uint32{0, MaxPayload + 1} {
		_, err = ReadStreamWithBufferLimit(&limitTestStream{}, limit)
		require.Error(t, err)
	}
}

// A rejected declared 20MB control frame does not allocate its advertised body.
func BenchmarkReadStreamWithBufferLimitReject(b *testing.B) {
	header := limitTestFrame(MaxPayload)
	s := &limitTestStream{reader: bytes.NewReader(header)}
	b.ReportAllocs()
	for b.Loop() {
		s.reader.Reset(header)
		if _, err := ReadStreamWithBufferLimit(s, 1); err == nil {
			b.Fatal("accepted oversize")
		}
	}
}

type deadlineLimitTestStream struct {
	network.Stream
	transport net.Conn
}

func (s *deadlineLimitTestStream) Read(p []byte) (int, error) {
	return s.transport.Read(p)
}

func (s *deadlineLimitTestStream) SetReadDeadline(deadline time.Time) error {
	return s.transport.SetReadDeadline(deadline)
}

func TestReadStreamWithBufferLimitAndDeadline(t *testing.T) {
	previous := ApplyDeadline.Load()
	ApplyDeadline.Store(true)
	t.Cleanup(func() { ApplyDeadline.Store(previous) })
	reader, writer := net.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	stream := &deadlineLimitTestStream{transport: reader}
	_, err := ReadStreamWithBufferLimitAndDeadline(stream, 1, time.Time{})
	require.Error(t, err)

	// An already expired absolute deadline must not become a fresh 40s wait.
	_, err = ReadStreamWithBufferLimitAndDeadline(stream, 1, time.Now().Add(-time.Second))
	var timeout net.Error
	require.ErrorAs(t, err, &timeout)
	require.True(t, timeout.Timeout())

	// The explicit-deadline path must retain the pre-allocation payload bound.
	done := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write(limitTestFrame(2))
		done <- writeErr
	}()
	_, err = ReadStreamWithBufferLimitAndDeadline(stream, 1, time.Now().Add(time.Second))
	require.ErrorContains(t, err, "exceeds protocol maximum")
	require.NoError(t, <-done)
}
