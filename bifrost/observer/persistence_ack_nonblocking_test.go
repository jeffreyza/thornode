package observer

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestErrataPersistenceAckCannotBlockQueue verifies that an abandoned
// acknowledgement cannot prevent Observer from processing the next delivery.
func TestErrataPersistenceAckCannotBlockQueue(t *testing.T) {
	storage, err := NewObserverStorage(filepath.Join(t.TempDir(), "observer"), config.LevelDBOptions{})
	if err != nil {
		t.Fatalf("open observer storage: %v", err)
	}
	defer storage.Close()

	observer := &Observer{
		logger:            zerolog.Nop(),
		storage:           storage,
		lock:              &sync.Mutex{},
		globalErrataQueue: make(chan types.ErrataBlock, 2),
		stopChan:          make(chan struct{}),
	}
	abandonedAck := make(chan error)
	done := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-observer.stopChan:
		default:
			close(observer.stopChan)
		}
		select {
		case <-abandonedAck:
		default:
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("errata processor did not stop during cleanup")
		}
	})
	go func() {
		observer.processErrataTx(context.Background())
		close(done)
	}()

	observer.globalErrataQueue <- types.ErrataBlock{
		DeliveryID: "abandoned-ack",
		Persisted:  abandonedAck,
	}
	nextAck := make(chan error, 1)
	observer.globalErrataQueue <- types.ErrataBlock{
		DeliveryID: "buffered-ack",
		Persisted:  nextAck,
	}

	select {
	case persistErr := <-nextAck:
		if persistErr != nil {
			t.Fatalf("persist second errata delivery: %v", persistErr)
		}
	case <-time.After(time.Second):
		t.Fatal("abandoned acknowledgement blocked the errata queue")
	}

	close(observer.stopChan)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("errata processor did not stop")
	}
}
