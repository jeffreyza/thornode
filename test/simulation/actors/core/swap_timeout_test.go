package core

import (
	"errors"
	"fmt"
	"testing"
	"time"

	rand "math/rand/v2"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestNewSwapActorTimeouts(t *testing.T) {
	t.Parallel()

	rng := newSwapTimeoutTestRNG(1, 2)

	ethToBtc := NewSwapActor(common.ETHAsset, common.BTCAsset, rng)
	if ethToBtc.Timeout != 2*time.Minute {
		t.Fatalf("expected standard swap timeout to be 2m, got %s", ethToBtc.Timeout)
	}

	ethToXmr := NewSwapActor(common.ETHAsset, common.XMRAsset, rng)
	if ethToXmr.Timeout != 8*time.Minute {
		t.Fatalf("expected XMR swap timeout to be 8m, got %s", ethToXmr.Timeout)
	}

	btcToTron := NewSwapActor(common.BTCAsset, common.TRXAsset, rng)
	if btcToTron.Timeout != 6*time.Minute {
		t.Fatalf("expected TRON-destination swap timeout to be 6m, got %s", btcToTron.Timeout)
	}

	// TRON source broadcasts can spend a significant part of the actor budget
	// waiting until the local fullnode can query the tx by id.
	trxToPolygon := NewSwapActor(common.TRXAsset, common.POLAsset, rng)
	if trxToPolygon.Timeout != 6*time.Minute {
		t.Fatalf("expected TRON-source swap timeout to be 6m, got %s", trxToPolygon.Timeout)
	}

	solToPolygon := NewSwapActor(common.SOLAsset, common.POLAsset, rng)
	if solToPolygon.Timeout != 4*time.Minute {
		t.Fatalf("expected SOL swap timeout to be 4m, got %s", solToPolygon.Timeout)
	}
}

func TestNewSwapMemolessActorTimeouts(t *testing.T) {
	t.Parallel()

	rng := newSwapTimeoutTestRNG(3, 4)

	ethToBtc := NewSwapMemolessActor(common.ETHAsset, common.BTCAsset, MemolessTypeRef, rng)
	if ethToBtc.Timeout != 5*time.Minute {
		t.Fatalf("expected standard memoless swap timeout to be 5m, got %s", ethToBtc.Timeout)
	}

	ethToXmr := NewSwapMemolessActor(common.ETHAsset, common.XMRAsset, MemolessTypeRef, rng)
	if ethToXmr.Timeout != 7*time.Minute {
		t.Fatalf("expected XMR memoless swap timeout to be 7m, got %s", ethToXmr.Timeout)
	}

	ethToSol := NewSwapMemolessActor(common.ETHAsset, common.SOLAsset, MemolessTypeRef, rng)
	if ethToSol.Timeout != 7*time.Minute {
		t.Fatalf("expected SOL memoless swap timeout to be 7m, got %s", ethToSol.Timeout)
	}
}

func newSwapTimeoutTestRNG(seed1, seed2 uint64) *rand.Rand {
	//nolint:gosec // deterministic simulation test RNG, not security-sensitive randomness.
	return rand.New(rand.NewPCG(seed1, seed2))
}

func TestNewAggregatorSwapActorTimeouts(t *testing.T) {
	t.Parallel()

	rng := newSwapTimeoutTestRNG(5, 6)

	atomToEth := NewAggregatorSwapActor(
		common.ATOMAsset,
		common.ETHAsset,
		"6f3848",
		"0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25",
		"1000000",
		rng,
	)
	if atomToEth.Timeout != 4*time.Minute {
		t.Fatalf("expected standard aggregator swap timeout to be 4m, got %s", atomToEth.Timeout)
	}

	solToEth := NewAggregatorSwapActor(
		common.SOLAsset,
		common.ETHAsset,
		"6f3848",
		"0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25",
		"1000000",
		rng,
	)
	if solToEth.Timeout != 12*time.Minute {
		t.Fatalf("expected SOL aggregator swap timeout to be 12m, got %s", solToEth.Timeout)
	}
}

func TestGetRecommendedMinAmountInFromError(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf(`(http://localhost:1317/thorchain/quote/swap) HTTP: 500 => {"code":2,"message":"amount less than min swap amount (recommended_min_amount_in: 941178450)","details":[]}`)
	got, ok := getRecommendedMinAmountInFromError(err)
	if !ok {
		t.Fatal("expected to extract recommended minimum amount")
	}

	want := sdkmath.NewUint(941178450)
	if !got.Equal(want) {
		t.Fatalf("expected recommended minimum %s, got %s", want, got)
	}
}

func TestRetryAmountAtRecommendedMin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current sdkmath.Uint
		min     sdkmath.Uint
		want    sdkmath.Uint
	}{
		{
			name:    "below minimum retries one percent above recommended minimum",
			current: sdkmath.NewUint(62500000),
			min:     sdkmath.NewUint(11895824400),
			want:    sdkmath.NewUint(12014782644),
		},
		{
			name:    "exact minimum retries one percent higher",
			current: sdkmath.NewUint(11895824400),
			min:     sdkmath.NewUint(11895824400),
			want:    sdkmath.NewUint(12014782644),
		},
		{
			name:    "stale minimum still nudges current amount upward with buffer",
			current: sdkmath.NewUint(11895824405),
			min:     sdkmath.NewUint(11895824400),
			want:    sdkmath.NewUint(12014782649),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retryAmountAtRecommendedMin(tt.current, tt.min)
			if !got.Equal(tt.want) {
				t.Fatalf("expected retry amount %s, got %s", tt.want, got)
			}
		})
	}
}

func TestAmountWithEncodedRefAtOrAbove(t *testing.T) {
	t.Parallel()

	asset, err := common.NewAsset("AVAX.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse asset: %v", err)
	}

	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			from: asset,
			to:   common.BTCAsset,
		},
		memolessRef: 2,
	}

	got := actor.amountWithEncodedRefAtOrAbove(sdkmath.NewUint(941178450))
	want := sdkmath.NewUint(941200002)
	if !got.Equal(want) {
		t.Fatalf("expected encoded amount %s, got %s", want, got)
	}
}

func TestMaxAmountWithEncodedRefAtOrAbove(t *testing.T) {
	t.Parallel()

	asset, err := common.NewAsset("AVAX.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse asset: %v", err)
	}

	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			from: asset,
			to:   common.BTCAsset,
		},
	}

	got := actor.maxAmountWithEncodedRefAtOrAbove(sdkmath.NewUint(941178450))
	want := sdkmath.NewUint(941199999)
	if !got.Equal(want) {
		t.Fatalf("expected max encoded amount %s, got %s", want, got)
	}
}

type memolessBroadcastClient struct {
	signCalls      int
	broadcastCalls int
	lastSigned     []byte
	signPayload    []byte
	signErr        error
	broadcastErr   error
	txid           string
}

func (m *memolessBroadcastClient) GetAccount(*common.PubKey) (*common.Account, error) {
	return &common.Account{}, nil
}

func (m *memolessBroadcastClient) SignTx(simtypes.SimTx) ([]byte, error) {
	m.signCalls++
	if m.signErr != nil {
		return nil, m.signErr
	}
	if m.signPayload != nil {
		return m.signPayload, nil
	}
	return []byte("fresh"), nil
}

func (m *memolessBroadcastClient) BroadcastTx(payload []byte) (string, error) {
	m.broadcastCalls++
	m.lastSigned = append([]byte(nil), payload...)
	if m.broadcastErr != nil {
		return "", m.broadcastErr
	}
	if m.txid == "" {
		return "ok", nil
	}
	return m.txid, nil
}

func TestSwapMemolessNonFundingErrorIsUnhandled(t *testing.T) {
	t.Parallel()

	_, handled := (&SwapMemolessActor{}).finishAfterMemolessFundingError(
		errors.New("temporary RPC failure"),
	)
	if handled {
		t.Fatal("expected non-funding error to remain unhandled")
	}
}

func TestSwapMemolessFundingErrorReleasesResources(t *testing.T) {
	drainXMRSwapSlotForTest()
	defer drainXMRSwapSlotForTest()

	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			from: common.ZECAsset,
			to:   common.XMRAsset,
		},
	}
	user := newTestSwapUser(t, "selected", common.ZECAsset, sdkmath.NewUint(100), common.XMRAsset, sdkmath.ZeroUint())
	if !user.Acquire() {
		t.Fatal("expected to lock selected user")
	}
	actor.user = user

	select {
	case xmrSwapSlot <- struct{}{}:
		actor.xmrSwapSlotAcquired = true
	default:
		t.Fatal("XMR swap slot was already held")
	}
	defer func() {
		if actor.xmrSwapSlotAcquired {
			actor.releaseXMRSwapSlot()
		}
		if actor.user != nil {
			actor.releaseUserSelection()
		}
	}()

	err := errors.New("fail to get unspent UTXO: insufficient balance: 0.000000 < 0.061000")
	result, ok := actor.finishAfterMemolessFundingError(err)
	if !ok {
		t.Fatal("expected memoless funding error to be handled")
	}
	if !result.Finish || result.Continue || result.Error != nil {
		t.Fatalf("expected non-fatal finish, got %+v", result)
	}
	if actor.user != nil {
		t.Fatal("expected selected user to be cleared")
	}
	if user.IsLocked() {
		t.Fatal("expected selected user to be released")
	}
	if actor.xmrSwapSlotAcquired {
		t.Fatal("expected XMR swap slot to be released")
	}

	select {
	case <-xmrSwapSlot:
		t.Fatal("expected XMR swap slot channel to be empty")
	default:
	}
}

func TestSwapMemolessBroadcastSwapUsesFreshSigningForSol(t *testing.T) {
	t.Parallel()

	client := &memolessBroadcastClient{
		signPayload: []byte("fresh-sol"),
		txid:        "sol-tx",
	}
	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			user: &simtypes.User{
				ChainClients: map[common.Chain]simtypes.LiteChainClient{
					common.SOLChain: client,
				},
			},
			from: common.SOLAsset,
		},
		swapTx:     simtypes.SimTx{Chain: common.SOLChain},
		signedSwap: []byte("stale-sol"),
	}

	result := actor.broadcastSwap(nil)
	if !result.Continue || result.Error != nil {
		t.Fatalf("expected SOL memoless broadcast to succeed, got %+v", result)
	}
	if client.signCalls != 1 {
		t.Fatalf("expected fresh SOL signing during broadcast, got %d sign calls", client.signCalls)
	}
	if client.broadcastCalls != 1 {
		t.Fatalf("expected 1 SOL broadcast attempt, got %d", client.broadcastCalls)
	}
	if string(client.lastSigned) != "fresh-sol" {
		t.Fatalf("expected SOL broadcast to use freshly signed payload, got %q", string(client.lastSigned))
	}
	if actor.swapTxID != "sol-tx" {
		t.Fatalf("expected SOL swap txid to be recorded, got %s", actor.swapTxID)
	}
}

func TestSwapMemolessBroadcastSwapSkipsPostSelectionFundingError(t *testing.T) {
	t.Parallel()

	fundingErr := errors.New("fail to get unspent UTXO: insufficient balance: 0.000000 < 0.061000")
	client := &memolessBroadcastClient{
		signErr: fundingErr,
	}
	user := newTestSwapUser(t, "selected", common.SOLAsset, sdkmath.NewUint(100), common.BTCAsset, sdkmath.ZeroUint())
	user.ChainClients[common.SOLChain] = client
	if !user.Acquire() {
		t.Fatal("expected to lock selected user")
	}

	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			user: user,
			from: common.SOLAsset,
			to:   common.BTCAsset,
		},
		swapTx: simtypes.SimTx{Chain: common.SOLChain},
	}
	defer func() {
		if actor.user != nil {
			actor.releaseUserSelection()
		}
	}()

	result := actor.broadcastSwap(nil)
	if !result.Finish || result.Continue || result.Error != nil {
		t.Fatalf("expected memoless broadcast funding failure to finish non-fatally, got %+v", result)
	}
	if actor.user != nil {
		t.Fatal("expected selected user to be cleared")
	}
	if user.IsLocked() {
		t.Fatal("expected selected user to be released")
	}
	if client.signCalls != 1 {
		t.Fatalf("expected one SOL signing attempt, got %d", client.signCalls)
	}
	if client.broadcastCalls != 0 {
		t.Fatalf("expected no SOL broadcast after signing failure, got %d", client.broadcastCalls)
	}
}

func TestSwapMemolessBroadcastSwapUsesPresignedPayloadForBtc(t *testing.T) {
	t.Parallel()

	client := &memolessBroadcastClient{
		txid: "btc-tx",
	}
	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			user: &simtypes.User{
				ChainClients: map[common.Chain]simtypes.LiteChainClient{
					common.BTCChain: client,
				},
			},
			from: common.BTCAsset,
		},
		swapTx:     simtypes.SimTx{Chain: common.BTCChain},
		signedSwap: []byte("stale-btc"),
	}

	result := actor.broadcastSwap(nil)
	if !result.Continue || result.Error != nil {
		t.Fatalf("expected BTC memoless broadcast to succeed, got %+v", result)
	}
	if client.signCalls != 0 {
		t.Fatalf("expected BTC memoless broadcast to reuse pre-signed payload, got %d sign calls", client.signCalls)
	}
	if client.broadcastCalls != 1 {
		t.Fatalf("expected 1 BTC broadcast attempt, got %d", client.broadcastCalls)
	}
	if string(client.lastSigned) != "stale-btc" {
		t.Fatalf("expected BTC broadcast to use cached payload, got %q", string(client.lastSigned))
	}
	if actor.swapTxID != "btc-tx" {
		t.Fatalf("expected BTC swap txid to be recorded, got %s", actor.swapTxID)
	}
}

func TestSwapMemolessStopsAfterUncertainTronBroadcast(t *testing.T) {
	for _, message := range []string{"confirmation unavailable", "insufficient funds in a failed receipt lookup"} {
		t.Run(message, func(t *testing.T) {
			client := &memolessBroadcastClient{broadcastErr: fmt.Errorf("%w: %s", simtypes.ErrSimTxUncertain, message)}
			user := newTestSwapUser(t, "selected", common.TRXAsset, sdkmath.NewUint(100), common.BTCAsset, sdkmath.ZeroUint())
			user.ChainClients[common.TRONChain] = client
			if !user.Acquire() {
				t.Fatal("expected to lock selected user")
			}
			t.Cleanup(func() {
				if user.IsLocked() {
					user.Release()
				}
			})
			a := &SwapMemolessActor{
				SwapActor: SwapActor{
					Actor: *simtypes.NewActor("uncertain TRON swap", newSwapTimeoutTestRNG(1, 2)),
					user:  user,
					from:  common.TRXAsset,
				},
				swapTx: simtypes.SimTx{Chain: common.TRONChain},
			}
			a.Timeout = time.Millisecond
			a.Interval = time.Millisecond
			verified := false
			a.Ops = []simtypes.Op{
				a.broadcastSwap,
				func(*simtypes.OpConfig) simtypes.OpResult { verified = true; return simtypes.OpResult{Finish: true} },
			}
			err := a.Execute(nil)
			if !errors.Is(err, simtypes.ErrSimTxUncertain) || client.signCalls != 1 || client.broadcastCalls != 1 || verified {
				t.Fatalf("uncertain swap retried, skipped or reached verification: signs=%d broadcasts=%d verified=%v err=%v", client.signCalls, client.broadcastCalls, verified, err)
			}
		})
	}
}
