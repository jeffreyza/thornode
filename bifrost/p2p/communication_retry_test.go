package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

type retryWriteStream struct {
	*MockNetworkStream
	written      bytes.Buffer
	writeErr     error
	readErr      error
	readStarted  chan struct{}
	closeErr     error
	closeStarted chan struct{}
	closeCount   atomic.Int32
	resetCount   atomic.Int32
}

func newRetryWriteStream() *retryWriteStream {
	return &retryWriteStream{MockNetworkStream: NewMockNetworkStream()}
}

func (s *retryWriteStream) Read([]byte) (int, error) {
	if s.readStarted != nil {
		close(s.readStarted)
	}
	if s.readErr != nil {
		return 0, s.readErr
	}
	return 0, io.EOF
}

func (s *retryWriteStream) Write(payload []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.written.Write(payload)
}

func (s *retryWriteStream) Close() error {
	s.closeCount.Add(1)
	if s.closeStarted != nil {
		select {
		case <-s.closeStarted:
		default:
			close(s.closeStarted)
		}
	}
	return s.closeErr
}

func (s *retryWriteStream) Reset() error {
	s.resetCount.Add(1)
	return nil
}

func framedPayload(payload []byte) []byte {
	framed := make([]byte, LengthHeader+len(payload))
	binary.LittleEndian.PutUint32(framed, uint32(len(payload)))
	copy(framed[LengthHeader:], payload)
	return framed
}

func TestStreamPolicyClassification(t *testing.T) {
	tests := []struct {
		msgType messages.THORChainTSSMessageType
		want    tssStreamPolicy
	}{
		{messages.TSSKeyGenMsg, tssStreamPolicy{attempts: 3}},
		{messages.TSSKeyGenVerMsg, tssStreamPolicy{attempts: 3}},
		{messages.TSSKeySignMsg, tssStreamPolicy{attempts: 1}},
		{messages.TSSKeySignVerMsg, tssStreamPolicy{attempts: 1}},
		{messages.TSSControlMsg, tssStreamPolicy{attempts: 1}},
		{messages.TSSTaskDone, tssStreamPolicy{attempts: 1}},
		{messages.TSSFrostDKGRound1Msg, tssStreamPolicy{attempts: 3, fullClose: true}},
		{messages.TSSFrostDKGRound2Msg, tssStreamPolicy{attempts: 3, fullClose: true}},
		{messages.TSSFrostDKGRound3Msg, tssStreamPolicy{attempts: 3, fullClose: true}},
		{messages.TSSFrostDKGResultMsg, tssStreamPolicy{attempts: 3, fullClose: true}},
		{messages.TSSFrostVerifyMsg, tssStreamPolicy{attempts: 1, fullClose: true}},
		{messages.TSSFrostPreprocessMsg, tssStreamPolicy{attempts: 1, fullClose: true}},
		{messages.TSSFrostShareMsg, tssStreamPolicy{attempts: 1, fullClose: true}},
		{messages.TSSFrostResultMsg, tssStreamPolicy{attempts: 1, fullClose: true}},
		{messages.Unknown, tssStreamPolicy{attempts: 1}},
	}
	for _, test := range tests {
		t.Run(test.msgType.String(), func(t *testing.T) {
			if got := streamPolicyFor(test.msgType); got != test.want {
				t.Fatalf("streamPolicyFor(%s)=%+v, want %+v", test.msgType, got, test.want)
			}
		})
	}
}

func TestFrostWriteFirstAttemptSuccessWritesAndClosesOnce(t *testing.T) {
	stream := newRetryWriteStream()
	var opens atomic.Int32
	comm := &Communication{
		stopChan:  make(chan struct{}),
		streamMgr: NewStreamMgr(),
		connectToOnePeerFn: func(peer.ID) (network.Stream, error) {
			opens.Add(1)
			return stream, nil
		},
	}
	payload := []byte("first-attempt-frost-dkg")
	attempts, err := comm.writeToStreamWithRetryDelay(
		peer.ID("remote-peer"), payload, "first-success", 3, true, time.Millisecond,
	)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if attempts != 1 || opens.Load() != 1 {
		t.Fatalf("attempts=%d opens=%d, want one", attempts, opens.Load())
	}
	if !bytes.Equal(stream.written.Bytes(), framedPayload(payload)) {
		t.Fatalf("written bytes=%x, want %x", stream.written.Bytes(), framedPayload(payload))
	}
	if stream.closeCount.Load() != 1 || stream.resetCount.Load() != 0 {
		t.Fatalf("terminal actions close=%d reset=%d", stream.closeCount.Load(), stream.resetCount.Load())
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("successful FROST write retained %d streams", got)
	}
}

func TestFrostEOFDrainFailureDoesNotRetryDeliveredPayload(t *testing.T) {
	stream := newRetryWriteStream()
	stream.readErr = errors.New("peer EOF unavailable")
	stream.readStarted = make(chan struct{})
	var opens atomic.Int32
	comm := &Communication{
		stopChan:  make(chan struct{}),
		streamMgr: NewStreamMgr(),
		connectToOnePeerFn: func(peer.ID) (network.Stream, error) {
			opens.Add(1)
			return stream, nil
		},
	}
	attempts, err := comm.writeToStreamWithRetryDelay(
		peer.ID("remote-peer"), []byte("delivered-before-eof-error"), "eof-error", 3, true, time.Millisecond,
	)
	if err != nil || attempts != 1 || opens.Load() != 1 {
		t.Fatalf("attempts=%d opens=%d err=%v, want one successful write", attempts, opens.Load(), err)
	}
	waitForTestSignal(t, stream.readStarted, "asynchronous peer EOF drain")
	deadline := time.Now().Add(time.Second)
	for stream.resetCount.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stream.closeCount.Load() != 1 || stream.resetCount.Load() != 1 {
		t.Fatalf("terminal actions close=%d reset=%d", stream.closeCount.Load(), stream.resetCount.Load())
	}
}

func TestFrostWriteRetriesFreshStreamsExactBytesAfterRelease(t *testing.T) {
	closeErr := errors.New("peer close failed")
	streams := []*retryWriteStream{
		newRetryWriteStream(),
		newRetryWriteStream(),
		newRetryWriteStream(),
	}
	streams[0].closeErr = closeErr
	streams[1].closeErr = closeErr
	thirdOpen := make(chan struct{})
	thirdGate := make(chan struct{})
	var openIndex atomic.Int32
	comm := &Communication{stopChan: make(chan struct{}), streamMgr: NewStreamMgr()}
	comm.connectToOnePeerFn = func(peer.ID) (network.Stream, error) {
		index := int(openIndex.Add(1)) - 1
		if index == 2 {
			close(thirdOpen)
			<-thirdGate
		}
		return streams[index], nil
	}
	payload := []byte("retry-exact-frost-dkg")
	type retryResult struct {
		attempts int
		err      error
	}
	result := make(chan retryResult, 1)
	go func() {
		attempts, err := comm.writeToStreamWithRetryDelay(
			peer.ID("remote-peer"), payload, "retry-after-release", 3, true, time.Millisecond,
		)
		result <- retryResult{attempts: attempts, err: err}
	}()
	waitForTestSignal(t, thirdOpen, "third retry stream")
	comm.ReleaseStream("retry-after-release")
	close(thirdGate)

	var got retryResult
	select {
	case got = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not complete")
	}
	if got.err != nil || got.attempts != 3 {
		t.Fatalf("attempts=%d err=%v, want third-attempt success", got.attempts, got.err)
	}
	wantBytes := framedPayload(payload)
	for index, stream := range streams {
		if !bytes.Equal(stream.written.Bytes(), wantBytes) {
			t.Fatalf("stream %d bytes=%x, want %x", index, stream.written.Bytes(), wantBytes)
		}
		if stream.closeCount.Load() != 1 {
			t.Fatalf("stream %d close count=%d", index, stream.closeCount.Load())
		}
		wantReset := int32(0)
		if index < 2 {
			wantReset = 1
		}
		if stream.resetCount.Load() != wantReset {
			t.Fatalf("stream %d reset count=%d, want %d", index, stream.resetCount.Load(), wantReset)
		}
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("retry completion after release retained %d streams", got)
	}
}

func TestFrostWriteErrorResetsWithoutTracking(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*retryWriteStream)
	}{
		{name: "payload write", configure: func(stream *retryWriteStream) { stream.writeErr = errors.New("write failed") }},
		{name: "write deadline", configure: func(stream *retryWriteStream) { stream.errSetWriteDeadLine = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalApplyDeadline := ApplyDeadline.Load()
			ApplyDeadline.Store(true)
			t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

			stream := newRetryWriteStream()
			test.configure(stream)
			comm := &Communication{
				stopChan:  make(chan struct{}),
				streamMgr: NewStreamMgr(),
				connectToOnePeerFn: func(peer.ID) (network.Stream, error) {
					return stream, nil
				},
			}
			if err := comm.writeToStream(peer.ID("remote-peer"), []byte("payload"), "write-error", true); err == nil {
				t.Fatal("expected write error")
			}
			if stream.closeCount.Load() != 0 || stream.resetCount.Load() != 1 {
				t.Fatalf("terminal actions close=%d reset=%d", stream.closeCount.Load(), stream.resetCount.Load())
			}
			if got := parkedStreamCount(comm.streamMgr); got != 0 {
				t.Fatalf("failed FROST write retained %d streams", got)
			}
		})
	}
}

func TestLegacyWriteSuccessRetainsStreamUntilCeremonyRelease(t *testing.T) {
	stream := newRetryWriteStream()
	comm := &Communication{
		stopChan:  make(chan struct{}),
		streamMgr: NewStreamMgr(),
		connectToOnePeerFn: func(peer.ID) (network.Stream, error) {
			return stream, nil
		},
	}
	payload := []byte("legacy-tss-payload")
	if err := comm.writeToStream(peer.ID("remote-peer"), payload, "legacy-session", false); err != nil {
		t.Fatalf("legacy write: %v", err)
	}
	if !bytes.Equal(stream.written.Bytes(), framedPayload(payload)) {
		t.Fatalf("written bytes=%x, want %x", stream.written.Bytes(), framedPayload(payload))
	}
	if stream.closeCount.Load() != 0 || stream.resetCount.Load() != 0 || parkedStreamCount(comm.streamMgr) != 1 {
		t.Fatalf("before release close=%d reset=%d parked=%d", stream.closeCount.Load(), stream.resetCount.Load(), parkedStreamCount(comm.streamMgr))
	}
	comm.ReleaseStream("legacy-session")
	if stream.closeCount.Load() != 0 || stream.resetCount.Load() != 1 || parkedStreamCount(comm.streamMgr) != 0 {
		t.Fatalf("after release close=%d reset=%d parked=%d", stream.closeCount.Load(), stream.resetCount.Load(), parkedStreamCount(comm.streamMgr))
	}
}

func TestFrostWriteShutdownDuringBackoffStopsRetries(t *testing.T) {
	stream := newRetryWriteStream()
	stream.closeErr = errors.New("close failed")
	stream.closeStarted = make(chan struct{})
	var opens atomic.Int32
	comm := &Communication{
		stopChan:  make(chan struct{}),
		streamMgr: NewStreamMgr(),
		connectToOnePeerFn: func(peer.ID) (network.Stream, error) {
			opens.Add(1)
			return stream, nil
		},
	}
	type retryResult struct {
		attempts int
		err      error
	}
	result := make(chan retryResult, 1)
	go func() {
		attempts, err := comm.writeToStreamWithRetryDelay(
			peer.ID("remote-peer"), []byte("payload"), "shutdown-backoff", 3, true, time.Hour,
		)
		result <- retryResult{attempts: attempts, err: err}
	}()
	waitForTestSignal(t, stream.closeStarted, "first failed close")
	close(comm.stopChan)
	select {
	case got := <-result:
		if got.err == nil || got.attempts != 1 {
			t.Fatalf("attempts=%d err=%v, want one failed attempt", got.attempts, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not interrupt retry backoff")
	}
	if opens.Load() != 1 {
		t.Fatalf("shutdown allowed %d stream opens", opens.Load())
	}
	if stream.closeCount.Load() != 1 || stream.resetCount.Load() != 1 {
		t.Fatalf("terminal actions close=%d reset=%d", stream.closeCount.Load(), stream.resetCount.Load())
	}
}

func TestFrostDKGRetryFanoutCostAt95Participants(t *testing.T) {
	const (
		participants = 95
		messageTypes = 4
	)
	firstAttemptPerNode := messageTypes * (participants - 1)
	firstAttemptNetwork := participants * firstAttemptPerNode
	failureMaxPerNode := streamWriteAttempts * firstAttemptPerNode
	failureMaxNetwork := participants * failureMaxPerNode
	if firstAttemptPerNode != 376 || firstAttemptNetwork != 35_720 {
		t.Fatalf("first-attempt cost per-node=%d network=%d", firstAttemptPerNode, firstAttemptNetwork)
	}
	if failureMaxPerNode != 1_128 || failureMaxNetwork != 107_160 {
		t.Fatalf("failure-only max per-node=%d network=%d", failureMaxPerNode, failureMaxNetwork)
	}
}

// Keysign deliberately makes one write attempt. A failed connection or write
// must terminate that attempt without poisoning a subsequent session's stream.
func TestFrostKeysignTransportFailureLeavesNextSessionUsable(t *testing.T) {
	for _, topic := range []messages.THORChainTSSMessageType{
		messages.TSSFrostPreprocessMsg, messages.TSSFrostShareMsg,
		messages.TSSFrostVerifyMsg, messages.TSSFrostResultMsg,
	} {
		t.Run(topic.String(), func(t *testing.T) {
			failed := newRetryWriteStream()
			failed.writeErr = errors.New("transient payload write failure")
			healthy := newRetryWriteStream()
			opens := 0
			comm := &Communication{logger: zerolog.Nop(), stopChan: make(chan struct{}), streamMgr: NewStreamMgr()}
			comm.connectToOnePeerFn = func(peer.ID) (network.Stream, error) {
				opens++
				switch opens {
				case 1:
					return nil, errors.New("transient connection failure")
				case 2:
					return failed, nil
				default:
					return healthy, nil
				}
			}
			policy := streamPolicyFor(topic)
			if policy.attempts != 1 || !policy.fullClose {
				t.Fatalf("keysign transport policy changed: %+v", policy)
			}
			for i := range 2 {
				attempts, writeErr := comm.writeToStreamWithRetryDelay(peer.ID("receiver"), []byte("failed-session"),
					fmt.Sprintf("failed-keysign-%d", i), policy.attempts, policy.fullClose, 0)
				if writeErr == nil || attempts != 1 || opens != i+1 {
					t.Fatalf("failure %d: attempts=%d opens=%d err=%v", i, attempts, opens, writeErr)
				}
				if parkedStreamCount(comm.streamMgr) != 0 {
					t.Fatal("failed keysign attempt retained a ceremony-owned stream")
				}
			}
			if failed.resetCount.Load() != 1 || failed.closeCount.Load() != 0 {
				t.Fatal("failed write did not reset its stream exactly once")
			}
			payload := frostTestFrame(t, topic, "subsequent-valid-keysign")
			attempts, writeErr := comm.writeToStreamWithRetryDelay(peer.ID("receiver"), payload,
				"subsequent-valid-keysign", policy.attempts, policy.fullClose, 0)
			if writeErr != nil || attempts != 1 || opens != 3 {
				t.Fatalf("subsequent session did not progress: attempts=%d opens=%d err=%v", attempts, opens, writeErr)
			}
			if !bytes.Equal(healthy.written.Bytes(), framedPayload(payload)) || healthy.closeCount.Load() != 1 || healthy.resetCount.Load() != 0 {
				t.Fatal("subsequent session did not write its exact frame and close once")
			}
			if parkedStreamCount(comm.streamMgr) != 0 {
				t.Fatal("subsequent session parked a successful FROST stream")
			}
		})
	}
}
