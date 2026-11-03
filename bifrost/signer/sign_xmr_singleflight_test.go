package signer

import (
	"errors"
	"sync"
	"testing"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type singleFlightKeygenBridge struct {
	fakeBridge

	mu          sync.Mutex
	batchSizes  []int
	keygenTimes []int64
	mimirs      map[string]int64
	mimirCalls  map[string]int
	height      int64
	heightErr   error
	mimirErrs   map[string]error
}

func (b *singleFlightKeygenBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mimirCalls == nil {
		b.mimirCalls = make(map[string]int)
	}
	b.mimirCalls[key]++
	if err := b.mimirErrs[key]; err != nil {
		return 0, err
	}
	if value, ok := b.mimirs[key]; ok {
		return value, nil
	}
	switch key {
	case constants.MimirKeyEnableMonovaultKeygen:
		return 1, nil
	case constants.MimirKeyHaltMonovaultKeygen:
		return 0, nil
	case constants.ChurnRetryInterval.String():
		return 1000, nil
	case constants.KeygenRetryInterval.String():
		return 0, nil
	default:
		return b.fakeBridge.GetMimir(key)
	}
}

func (b *singleFlightKeygenBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.heightErr != nil {
		return 0, b.heightErr
	}
	if b.height != 0 {
		return b.height, nil
	}
	return 100, nil
}

func (b *singleFlightKeygenBridge) setMimir(key string, value int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mimirs == nil {
		b.mimirs = make(map[string]int64)
	}
	b.mimirs[key] = value
}

func (b *singleFlightKeygenBridge) setHeight(height int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.height = height
}

func (b *singleFlightKeygenBridge) mimirReadCount(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mimirCalls[key]
}

func (b *singleFlightKeygenBridge) Broadcast(msgs ...sdktypes.Msg) (common.TxID, error) {
	b.mu.Lock()
	b.batchSizes = append(b.batchSizes, len(msgs))
	b.mu.Unlock()
	return common.TxID("single-flight-keygen-tx"), nil
}

func (b *singleFlightKeygenBridge) broadcastCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batchSizes)
}

func (b *singleFlightKeygenBridge) GetKeygenStdTx(
	_ common.PubKey,
	_ []byte,
	_ []byte,
	_ []types2.Blame,
	_ common.PubKeys,
	_ types2.KeygenType,
	_ common.Chains,
	_ int64,
	keygenTime int64,
	_ common.PubKey,
	_ []byte,
	_ ...any,
) (sdktypes.Msg, error) {
	b.mu.Lock()
	b.keygenTimes = append(b.keygenTimes, keygenTime)
	b.mu.Unlock()
	return &types2.MsgTssPool{}, nil
}

func (b *singleFlightKeygenBridge) reportedKeygenTimes() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.keygenTimes...)
}

type timedXMRKeygenClient struct {
	*MockChainClient
	advanceClock func()
	result       chainclienttypes.CustomKeygenResult
}

func (c *timedXMRKeygenClient) CustomKeygen() bool { return true }

func (c *timedXMRKeygenClient) Keygen(chainclienttypes.CustomKeygenRequest) (chainclienttypes.CustomKeygenResult, error) {
	c.advanceClock()
	return c.result, nil
}

// controlledXMRKeygenClient keeps the first hook active until a test releases
// it, making overlap deterministic without adding timing seams to production.
type controlledXMRKeygenClient struct {
	*MockChainClient

	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	firstRelease chan struct{}
	releaseOnce  sync.Once
	results      []chainclienttypes.CustomKeygenResult
	errs         []error
	heights      []int64
}

func (c *controlledXMRKeygenClient) CustomKeygen() bool { return true }

func (c *controlledXMRKeygenClient) Keygen(req chainclienttypes.CustomKeygenRequest) (chainclienttypes.CustomKeygenResult, error) {
	c.mu.Lock()
	call := c.calls
	c.calls++
	c.heights = append(c.heights, req.BlockHeight)
	result := c.results[call]
	err := c.errs[call]
	if call == 0 {
		close(c.firstStarted)
	}
	c.mu.Unlock()

	if call == 0 {
		<-c.firstRelease
	}
	return result, err
}

func (c *controlledXMRKeygenClient) calledHeights() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.heights...)
}

func (c *controlledXMRKeygenClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *controlledXMRKeygenClient) releaseFirst() {
	c.releaseOnce.Do(func() { close(c.firstRelease) })
}

func newSingleFlightTestSigner(client chainclients.ChainClient) (*Signer, *singleFlightKeygenBridge) {
	bridge := &singleFlightKeygenBridge{fakeBridge: fakeBridge{}, height: 2700}
	return &Signer{
		stopChan: make(chan struct{}),
		chains: map[common.Chain]chainclients.ChainClient{
			common.XMRChain: client,
		},
		thorchainBridge: bridge,
		pubkeyMgr:       pubkeymanager.NewMockPoolAddressValidator(),
		logger:          log.With().Str("module", "signer-single-flight-test").Logger(),
		errCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "test",
			Subsystem: "signer_xmr_single_flight",
			Name:      "errors_total",
			Help:      "errors in XMR signer single-flight tests",
		}, []string{"reason", "height"}),
	}, bridge
}

func newSingleFlightXMRKeygen(t *testing.T, height int64) types2.KeygenBlock {
	t.Helper()
	memberA := types2.GetRandomPubKey()
	memberB := types2.GetRandomPubKey()
	keygen, err := types2.NewKeygen(
		height,
		[]string{memberA.String(), memberB.String()},
		types2.KeygenType_XMRMonovaultKeygen,
	)
	if err != nil {
		t.Fatalf("create XMR keygen: %v", err)
	}
	return singleKeygenBlock(height, keygen)
}

func waitForSingleFlightReturn(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not return promptly", operation)
	}
}

// TestSignerXMRKeygenReportsElapsedDuration proves the result reports time
// spent inside the sidecar hook instead of sampling twice before DKG starts.
func TestSignerXMRKeygenReportsElapsedDuration(t *testing.T) {
	originalKeygenNowFn := keygenNowFn
	currentTime := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	keygenNowFn = func() time.Time { return currentTime }
	t.Cleanup(func() { keygenNowFn = originalKeygenNowFn })

	const elapsed = 1500 * time.Millisecond
	frostPubKey := types2.GetRandomPubKey()
	client := &timedXMRKeygenClient{
		MockChainClient: &MockChainClient{},
		advanceClock: func() {
			currentTime = currentTime.Add(elapsed)
		},
		result: chainclienttypes.CustomKeygenResult{PoolPubKeyFrost: frostPubKey},
	}
	signer, bridge := newSingleFlightTestSigner(client)

	signer.processKeygenBlock(newSingleFlightXMRKeygen(t, 2700))

	if got := bridge.reportedKeygenTimes(); len(got) != 1 || got[0] != elapsed.Milliseconds() {
		t.Fatalf("reported keygen durations %v, want [%d]", got, elapsed.Milliseconds())
	}
}

// TestSignerXMRKeygenSingleFlight models the normal consumer and a retry
// goroutine arriving at their shared processKeygenBlock entry simultaneously.
// Only the admitted hook may run, while later sequential attempts remain valid.
func TestSignerXMRKeygenSingleFlight(t *testing.T) {
	tests := []struct {
		name                 string
		firstErr             error
		wantBroadcastBatches int
	}{
		{name: "release after success", wantBroadcastBatches: 2},
		{name: "release after failure", firstErr: errors.New("DKG failed"), wantBroadcastBatches: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frostPubKey := types2.GetRandomPubKey()
			client := &controlledXMRKeygenClient{
				MockChainClient: &MockChainClient{},
				firstStarted:    make(chan struct{}),
				firstRelease:    make(chan struct{}),
				results: []chainclienttypes.CustomKeygenResult{
					{PoolPubKeyFrost: frostPubKey},
					{PoolPubKeyFrost: frostPubKey},
				},
				errs: []error{test.firstErr, nil},
			}
			t.Cleanup(client.releaseFirst)
			signer, bridge := newSingleFlightTestSigner(client)
			block := newSingleFlightXMRKeygen(t, 2700)

			activeDone := make(chan struct{})
			go func() {
				defer close(activeDone)
				signer.processKeygenBlock(block)
			}()
			waitForSingleFlightReturn(t, client.firstStarted, "active XMR keygen start")

			contenderDone := make(chan struct{})
			go func() {
				defer close(contenderDone)
				signer.processKeygenBlock(block)
			}()
			waitForSingleFlightReturn(t, contenderDone, "overlapping XMR keygen contender")

			if got := client.callCount(); got != 1 {
				t.Fatalf("overlap started %d custom keygen hooks, want 1", got)
			}
			select {
			case <-activeDone:
				t.Fatal("active XMR keygen returned before its hook was released")
			default:
			}
			time.Sleep(20 * time.Millisecond)
			if got := client.callCount(); got != 1 {
				t.Fatalf("rejected contender retried in a hot loop: got %d hook calls", got)
			}

			client.releaseFirst()
			waitForSingleFlightReturn(t, activeDone, "active XMR keygen completion")

			// No completion cache is retained: a later delivery or scheduled
			// retry may submit a result that THORNode has not yet accepted.
			signer.processKeygenBlock(block)
			if got := client.callCount(); got != 2 {
				t.Fatalf("sequential XMR keygen made %d total hook calls, want 2", got)
			}
			if got := bridge.broadcastCount(); got != test.wantBroadcastBatches {
				t.Fatalf("got %d successful result broadcasts, want %d", got, test.wantBroadcastBatches)
			}
		})
	}
}

// TestSignerXMRKeygenSingleFlightDoesNotSerializeNonXMR verifies the XMR-only
// admission rule leaves ordinary TSS keygen on its existing execution path.
func TestSignerXMRKeygenSingleFlightDoesNotSerializeNonXMR(t *testing.T) {
	frostPubKey := types2.GetRandomPubKey()
	client := &controlledXMRKeygenClient{
		MockChainClient: &MockChainClient{},
		firstStarted:    make(chan struct{}),
		firstRelease:    make(chan struct{}),
		results: []chainclienttypes.CustomKeygenResult{
			{PoolPubKeyFrost: frostPubKey},
		},
		errs: []error{nil},
	}
	t.Cleanup(client.releaseFirst)
	signer, _ := newSingleFlightTestSigner(client)
	signer.tssKeygen = &tss.KeyGen{}
	xmrBlock := newSingleFlightXMRKeygen(t, 2700)

	xmrDone := make(chan struct{})
	go func() {
		defer close(xmrDone)
		signer.processKeygenBlock(xmrBlock)
	}()
	waitForSingleFlightReturn(t, client.firstStarted, "active XMR keygen start")

	regularKeygen, err := types2.NewKeygen(2700, nil, types2.KeygenType_AsgardKeygen)
	if err != nil {
		t.Fatalf("create regular keygen: %v", err)
	}
	regularDone := make(chan struct{})
	go func() {
		defer close(regularDone)
		signer.processKeygenBlock(singleKeygenBlock(2700, regularKeygen))
	}()
	waitForSingleFlightReturn(t, regularDone, "non-XMR keygen while XMR is active")
	if got := client.callCount(); got != 1 {
		t.Fatalf("non-XMR processing changed XMR hook count to %d, want 1", got)
	}

	client.releaseFirst()
	waitForSingleFlightReturn(t, xmrDone, "active XMR keygen completion")
}
