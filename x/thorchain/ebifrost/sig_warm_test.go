package ebifrost

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/stretchr/testify/require"

	common "gitlab.com/thorchain/thornode/v3/common"
)

type sigWarmCall struct {
	pubKey, sig, payload string
}

// recordSigWarm replaces sigWarmVerify with a recorder for the test, once
// warming left over from earlier tests has released its slots.
func recordSigWarm(t *testing.T) chan sigWarmCall {
	waitSigWarmSlots(t)
	calls := make(chan sigWarmCall, 64)
	orig := sigWarmVerify
	sigWarmVerify = func(pubKey, sig, payload []byte) bool {
		calls <- sigWarmCall{string(pubKey), string(sig), string(payload)}
		return true
	}
	t.Cleanup(func() { sigWarmVerify = orig })
	return calls
}

// waitSigWarmSlots waits until no warming goroutine holds a slot, so the next
// Send* is not skipped for lack of a free one.
func waitSigWarmSlots(t *testing.T) {
	require.Eventually(t, func() bool { return len(sigWarmSlots) == 0 }, 10*time.Second, time.Millisecond, "warm slot not released")
}

// nextSigWarm returns the next warmed signature once its worker has released
// its slot.
func nextSigWarm(t *testing.T, calls chan sigWarmCall) sigWarmCall {
	var call sigWarmCall
	select {
	case call = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("signature was not warmed")
	}
	waitSigWarmSlots(t)
	return call
}

// TestSendQuorumWarmsSignatures checks every Send* warms each attestation
// against the payload its quorum handler verifies.
func TestSendQuorumWarmsSignatures(t *testing.T) {
	calls := recordSigWarm(t)
	ebs := NewEnshrinedBifrost(codec.NewProtoCodec(codectypes.NewInterfaceRegistry()), log.NewNopLogger(), EBifrostConfig{
		Enable:  true,
		Address: "localhost:50051",
	})
	att := createTestAttestation("pubkey1", "sig1")

	tx := createTestQuorumTx(common.BTCChain, "tx1", true, []*common.Attestation{att})
	want, err := tx.ObsTx.GetSignablePayloadWithInbound(true)
	require.NoError(t, err)
	_, err = ebs.SendQuorumTx(context.Background(), tx)
	require.NoError(t, err)
	require.Equal(t, sigWarmCall{"pubkey1", "sig1", string(want)}, nextSigWarm(t, calls))

	nf := createTestNetworkFee(common.BTCChain, 10, []*common.Attestation{att})
	want, err = nf.NetworkFee.GetSignablePayload()
	require.NoError(t, err)
	_, err = ebs.SendQuorumNetworkFee(context.Background(), nf)
	require.NoError(t, err)
	require.Equal(t, string(want), nextSigWarm(t, calls).payload)

	s := createTestSolvency(common.BTCChain, 10, []*common.Attestation{att})
	solvency := *s.Solvency
	want, err = solvency.GetSignablePayload()
	require.NoError(t, err)
	_, err = ebs.SendQuorumSolvency(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, string(want), nextSigWarm(t, calls).payload)
	require.True(t, ebs.solvencyCache.items[0].Item.Solvency.Id.IsEmpty(), "warming mutated the cached solvency")

	e := createTestErrataTx(common.BTCChain, "tx1", []*common.Attestation{att})
	want, err = e.ErrataTx.GetSignablePayload()
	require.NoError(t, err)
	_, err = ebs.SendQuorumErrataTx(context.Background(), e)
	require.NoError(t, err)
	require.Equal(t, string(want), nextSigWarm(t, calls).payload)

	pf := &common.PriceFeed{Version: []byte{1}, Time: 1}
	want, err = pf.GetSignablePayload()
	require.NoError(t, err)
	_, err = ebs.SendQuorumPriceFeedBatch(context.Background(), createTestPriceFeedBatch([]*common.QuorumPriceFeed{
		{PriceFeed: pf, Attestations: []*common.Attestation{att}},
		{Attestations: []*common.Attestation{att}}, // no feed to sign: skipped
	}))
	require.NoError(t, err)
	require.Equal(t, string(want), nextSigWarm(t, calls).payload)

	select {
	case call := <-calls:
		t.Fatalf("unexpected warm call %+v", call)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestWarmAttestationSigsNeverBlocks checks warming is skipped, not queued,
// when every slot is busy, and that malformed attestations are tolerated.
func TestWarmAttestationSigsNeverBlocks(t *testing.T) {
	calls := recordSigWarm(t)

	for i := 0; i < cap(sigWarmSlots); i++ {
		sigWarmSlots <- struct{}{}
	}
	warmAttestationSigs(sigWarmJob{[]byte("payload"), []*common.Attestation{createTestAttestation("pk", "sig")}})
	for i := 0; i < cap(sigWarmSlots); i++ {
		<-sigWarmSlots
	}
	select {
	case call := <-calls:
		t.Fatalf("warmed with no free slot: %+v", call)
	case <-time.After(100 * time.Millisecond):
	}

	sigWarmVerify = func(pubKey, sig, payload []byte) bool {
		if string(pubKey) == "panic" {
			panic("malformed key")
		}
		calls <- sigWarmCall{string(pubKey), string(sig), string(payload)}
		return true
	}
	warmAttestationSigs(sigWarmJob{[]byte("payload"), []*common.Attestation{
		nil,
		{PubKey: []byte("pk")},
		createTestAttestation("panic", "sig"),
		createTestAttestation("pk", "sig"),
	}})
	require.Equal(t, sigWarmCall{"pk", "sig", "payload"}, nextSigWarm(t, calls))
}
