package p2p

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// stallingHandlerStream serves its buffered bytes, then blocks like a peer
// that stopped mid-frame, until the stream is reset.
type stallingHandlerStream struct {
	*lifecycleHandlerStream
	stalled   chan struct{}
	unblock   chan struct{}
	stallOnce sync.Once
	resetOnce sync.Once
}

func (s *stallingHandlerStream) Read(payload []byte) (int, error) {
	if s.Buffer.Len() > 0 {
		return s.lifecycleHandlerStream.Read(payload)
	}
	s.stallOnce.Do(func() { close(s.stalled) })
	<-s.unblock
	return 0, errors.New("stream reset")
}

func (s *stallingHandlerStream) Reset() error {
	s.resetOnce.Do(func() { close(s.unblock) })
	return s.lifecycleHandlerStream.Reset()
}

// Cancelling a ceremony resets a FROST stream that stalled mid-frame after
// its preflight charge, instead of leaving its buffer and handler until the
// read deadline. The reset is the stream's only terminal action.
func TestFrostCancellationResetsStalledFrameRead(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	topic := messages.TSSFrostShareMsg
	msgID := "cancel-stalled-read"
	remote := peer.ID("stalling-peer")
	comm := newHandlerCommunication(topic, msgID, []peer.ID{remote})
	subscription := comm.getSubscriber(topic, msgID)

	prefix := tssFramePrefix(t, topic, msgID)
	stream := &stallingHandlerStream{
		lifecycleHandlerStream: newLifecycleHandlerStream(remote, nil),
		stalled:                make(chan struct{}),
		unblock:                make(chan struct{}),
	}
	stream.Buffer.Reset()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, uint32(len(prefix)+1024))
	_, _ = stream.Buffer.Write(header)
	_, _ = stream.Buffer.Write(prefix)

	handled := make(chan struct{})
	go func() {
		comm.handleStreamTss(stream)
		close(handled)
	}()
	waitForTestSignal(t, stream.stalled, "the handler to stall mid-frame")
	subscription.lock.Lock()
	charged := subscription.peerCounts[remote]
	subscription.lock.Unlock()
	if charged != 1 {
		t.Fatalf("stalled frame was not charged at preflight: %d", charged)
	}

	comm.CancelSubscribe(topic, msgID)
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation left a stalled FROST read waiting for its deadline")
	}
	if stream.resetCount.Load() != 1 || stream.closeCount.Load() != 0 {
		t.Fatalf("terminal actions reset=%d close=%d, want exactly one reset", stream.resetCount.Load(), stream.closeCount.Load())
	}
	if got := parkedStreamCount(comm.streamMgr); got != 0 {
		t.Fatalf("cancelled FROST stream parked %d streams", got)
	}
}
