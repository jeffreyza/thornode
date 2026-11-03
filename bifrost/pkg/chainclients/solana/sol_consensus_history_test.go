package solana

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/require"
	. "gopkg.in/check.v1"

	soltypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// sol_consensus_history_test.go — #3344 regression coverage
//
// consensusBlockHash picks the finalized SOL slot at target = t(keysignHeight) - margin.
// target never moves (it is a pure function of the consensus keysign height), but the
// OLDEST finalized block an RPC can serve only ever moves FORWARD as it prunes. Once
// that history floor passes target the search can never bracket it again at this
// height, and before this fix the signer answered by retaining the outbound and
// re-running the same doomed search every pass for its whole signing window. A
// scheduled SOL migration burned that window without ever producing a keysign, and
// migrations are only re-scheduled after SigningTransactionPeriod x
// StaleMigratePendingSigningPeriodMultiplier blocks, so the churn never completed.
////////////////////////////////////////////////////////////////////////////////////////

// strandedHistoryScenario models an RPC whose retained finalized history begins AFTER
// the consensus target: the tip is well past target, but every block it can still serve
// is newer than it. This is the mocknet validator's pruned ledger, and any non-archival
// provider on mainnet. Its 201 retained blocks (1000..1200) all postdate the target, so
// they PROVE the anchor is more than MAX_PROCESSING_AGE blocks old.
func strandedHistoryScenario(target int64) solRPCScenario {
	return solRPCScenario{tip: 1200, floorSlot: 1000, alignSlot: 950, target: target}
}

// shallowHistoryScenario is the same stranded shape with a SHALLOW retained history:
// the target is still unreachable, but only 101 blocks (1100..1200) sit above it —
// fewer than MAX_PROCESSING_AGE — so expiry is NOT proven and the outbound must be
// retained for retry rather than dropped.
func shallowHistoryScenario(target int64) solRPCScenario {
	return solRPCScenario{tip: 1200, floorSlot: 1100, alignSlot: 950, target: target}
}

// slowClusterScenario is shallowHistoryScenario under a 3s/slot cluster slowdown: the
// 250 slots between target and tip span 750s of WALL CLOCK, many times Solana's nominal
// ~60s blockhash liveness, while only 101 blocks separate the anchor from the tip. A
// wall-clock or height-age budget reads this as long dead; the block count — the unit
// Solana actually uses — says expiry is unproven.
func slowClusterScenario(target int64) solRPCScenario {
	sc := shallowHistoryScenario(target)
	sc.blockTime = func(slot uint64) (int64, bool) {
		if !sc.hasBlock(slot) {
			return 0, false
		}
		return target + (int64(slot)-sc.alignSlot)*3, true
	}
	return sc
}

// nullOldestScenario is strandedHistoryScenario whose OLDEST retained slot answers
// getBlockTime with null. The listing is still 201 blocks deep, but an unordered oldest
// slot cannot be placed after target, so those blocks are not known to postdate the
// anchor: the count alone is never the proof.
func nullOldestScenario(target int64) solRPCScenario {
	sc := strandedHistoryScenario(target)
	sc.blockTime = func(slot uint64) (int64, bool) {
		if slot == sc.floorSlot || !sc.hasBlock(slot) {
			return 0, false
		}
		return sc.blockTimeOf(slot), true
	}
	return sc
}

// retainedHistoryScenario is the same node once the blocks around target are available
// again. S* is slot 950.
func retainedHistoryScenario(target int64) solRPCScenario {
	return solRPCScenario{tip: 1200, floorSlot: 1, alignSlot: 950, target: target}
}

// skippedRunScenario models a long run of SKIPPED slots sitting between the finalized
// tip and the consensus target, inside history the node still RETAINS. getBlocks over
// any window that lands inside the run returns the same oldest slot (the first one
// above the gap) no matter how wide the window is — the exact shape pruning produces —
// yet the blocks around target are still there and the search MUST reach them.
//
// tip 1840, no blocks in 951..1800, target at slot 950: lookbacks of 400 (startI 1440)
// and 800 (startI 1040) both land inside the gap and both report oldest == 1801; only
// a lookback of 1600 (startI 240) reaches slot 950.
func skippedRunScenario(target int64) solRPCScenario {
	return solRPCScenario{
		tip:       1840,
		floorSlot: 1,
		skipFrom:  951,
		skipTo:    1800,
		alignSlot: 950,
		target:    target,
	}
}

// TestConsensusBlockHashCrossesSkippedSlotRun is the other half of the history-floor
// stop: an unreachable-looking window must only end the search when the RPC CONFIRMS it
// has nothing older. Two successive windows returning the same oldest slot is not that
// proof — a skipped-slot run reads identically — so the search keeps galloping and
// finds the target that was available all along.
func TestConsensusBlockHashCrossesSkippedSlotRun(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	client, m := newConsensusCacheClient(t, skippedRunScenario(target))

	hash, slot, err := client.consensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, int64(950), slot, "must gallop past the skipped run to S*")
	require.Equal(t, mockBlockhashForSlot(950), hash)

	// It had to widen past the two windows that both bottomed out at slot 1801.
	require.GreaterOrEqual(t, m.count("getBlocks"), 3)
}

// TestConsensusBlockHashSkippedRunAgreesWithGaplessNode pins the determinism invariant
// across the new stop condition: a node whose RPC happens to serve the skipped run as a
// plain gap must still pick the SAME slot and hash as one with an unbroken listing.
func TestConsensusBlockHashSkippedRunAgreesWithGaplessNode(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds

	skipped, _ := newConsensusCacheClient(t, skippedRunScenario(target))
	gapless, _ := newConsensusCacheClient(t, solRPCScenario{tip: 1840, floorSlot: 1, alignSlot: 950, target: target})

	skippedHash, skippedSlot, err := skipped.consensusBlockHash(100)
	require.NoError(t, err)

	gaplessHash, gaplessSlot, err := gapless.consensusBlockHash(100)
	require.NoError(t, err)

	require.Equal(t, gaplessSlot, skippedSlot)
	require.Equal(t, gaplessHash, skippedHash)
}

// keysignHeight is the consensus keysign height these tests derive from; the mock
// bridge reports the same block time for every height.
const keysignHeight = int64(838)

// TestConsensusBlockHashStopsAtHistoryFloor pins the search-cost half of the fix: when
// the window already reaches the oldest slot the RPC retains — as getFirstAvailableBlock
// reports it — no wider window can return anything older, so the search stops there
// instead of climbing the whole 400 -> 6400 lookback ladder on every retry pass.
func TestConsensusBlockHashStopsAtHistoryFloor(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	client, m := newConsensusCacheClient(t, shallowHistoryScenario(target))

	hash, slot, err := client.consensusBlockHash(100)
	require.NoError(t, err)
	require.Empty(t, hash)
	require.Zero(t, slot)
	require.LessOrEqual(t, m.count("getBlocks"), 2)
	// The floor is a property of the node, not of the window: ask once, not per rung.
	require.LessOrEqual(t, m.count("getFirstAvailableBlock"), 1)
}

// TestConsensusBlockHashSearchesOnWhenHistoryFloorUnknown covers the degraded provider:
// if getFirstAvailableBlock cannot be answered, pruning is UNPROVEN, so the search must
// fall back to the full bounded gallop rather than treat the failure as a floor.
func TestConsensusBlockHashSearchesOnWhenHistoryFloorUnknown(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	sc := skippedRunScenario(target)
	sc.noHistoryFloor = true
	client, m := newConsensusCacheClient(t, sc)

	hash, slot, err := client.consensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, int64(950), slot)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.GreaterOrEqual(t, m.count("getBlocks"), 3)
}

// TestConsensusBlockHashDropsOnlyOnProvenBlockExpiry is the recovery half, and the
// reason the verdict is denominated in BLOCKS. All three views below give up on the
// search; what separates a drop from a retry is whether the finalized listing PROVES
// more than solMaxProcessingAgeBlocks blocks were produced after the anchor.
func TestConsensusBlockHashDropsOnlyOnProvenBlockExpiry(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds

	for _, tc := range []struct {
		name      string
		scenario  solRPCScenario
		wantDrop  bool
		wantWhy   string
		wantCount int
	}{
		{
			name:      "201 blocks above target proves expiry",
			scenario:  strandedHistoryScenario(target),
			wantDrop:  true,
			wantWhy:   "the anchor is provably past MAX_PROCESSING_AGE",
			wantCount: 201,
		},
		{
			name:     "101 blocks above target proves nothing",
			scenario: shallowHistoryScenario(target),
			wantWhy:  "fewer than MAX_PROCESSING_AGE blocks, so the hash may still be live",
		},
		{
			// The case a 2-minute wall-clock budget got wrong: minutes have passed,
			// but the cluster has not produced enough blocks to expire the anchor.
			name:     "slow cluster is not expiry",
			scenario: slowClusterScenario(target),
			wantWhy:  "a slowdown stretches blocks over wall clock, it does not age them",
		},
		{
			// 201 blocks again, but an unordered oldest slot cannot place them after
			// the anchor, so the count proves nothing.
			name:     "deep listing with an unordered oldest slot proves nothing",
			scenario: nullOldestScenario(target),
			wantWhy:  "a null oldest block time leaves the listing unordered against target",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newConsensusCacheClient(t, tc.scenario)

			hash, slot, err := client.consensusBlockHash(keysignHeight)
			require.Empty(t, hash)
			require.Zero(t, slot)
			if !tc.wantDrop {
				require.NoError(t, err, tc.wantWhy)
				return
			}
			require.ErrorIs(t, err, errAnchorExpired, tc.wantWhy)
		})
	}
}

// TestConsensusBlockHashIgnoresKeysignHeightAge pins the unit itself. How far behind
// the THORChain tip a keysign height sits is not evidence about Solana's blockhash
// queue: a height thousands of blocks old still derives its hash, and the derivation
// never reads the tip at all. The authority on whether that hash can still land is
// SignTx's IsBlockhashValid gate against the processed bank.
func TestConsensusBlockHashIgnoresKeysignHeightAge(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	client, _ := newConsensusCacheClient(t, retainedHistoryScenario(target))
	bridge := &mockSOLThorchainBridge{thorHeight: keysignHeight + 1_000_000}
	client.bridge = bridge

	hash, slot, err := client.consensusBlockHash(keysignHeight)
	require.NoError(t, err)
	require.Equal(t, int64(950), slot)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.Zero(t, bridge.blockHeightCalls, "the THORChain tip must not enter the derivation")
}

// TestConsensusBlockHashDeterministicAcrossHistoryFloors is the invariant this fix must
// not weaken: two signers whose RPCs retain DIFFERENT amounts of history still derive
// the same blockhash, as long as both windows bracket the target.
func TestConsensusBlockHashDeterministicAcrossHistoryFloors(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds

	archival, _ := newConsensusCacheClient(t, retainedHistoryScenario(target))
	pruned, _ := newConsensusCacheClient(t, solRPCScenario{tip: 1190, floorSlot: 900, alignSlot: 950, target: target})

	archivalHash, archivalSlot, err := archival.consensusBlockHash(100)
	require.NoError(t, err)

	prunedHash, prunedSlot, err := pruned.consensusBlockHash(100)
	require.NoError(t, err)

	require.Equal(t, archivalHash, prunedHash)
	require.Equal(t, archivalSlot, prunedSlot)
}

// TestSignTxStrandedHistoryRetainsThenSignsWhenHistoryReturns is the end-to-end #3344
// case against the full client. While the keysign height is still young the outbound is
// RETAINED (an error, so the signer retries — a history floor can be a failed-over
// backend that comes back), and once the finalized blocks the search needs are
// available the very same outbound signs and broadcasts.
func (s *SolTestSuite) TestSignTxStrandedHistoryRetainsThenSignsWhenHistoryReturns(c *C) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	mock, server := newCountingSOLRPCServer(shallowHistoryScenario(target))
	defer server.Close()

	e, pub, pubkeyMgr := s.newSolCacheTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()

	txOut := newSolCacheTestTxOut(pub)

	rawTx, _, _, err := e.SignTx(txOut, keysignHeight)
	c.Assert(err, NotNil) // retained for retry: not dropped, not signed
	c.Assert(rawTx, IsNil)
	c.Assert(mock.count("sendTransaction"), Equals, 0)
	c.Assert(e.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, false)

	// The blocks the search needs are back in range: the outbound signs and broadcasts.
	mock.setScenario(retainedHistoryScenario(target))

	rawTx, _, _, err = e.SignTx(txOut, keysignHeight)
	c.Assert(err, IsNil)
	c.Assert(rawTx, NotNil)

	txSig, err := e.BroadcastTx(txOut, rawTx)
	c.Assert(err, IsNil)
	c.Assert(txSig, Equals, "broadcast-sig")
	c.Assert(mock.count("sendTransaction"), Equals, 1)
}

// TestBroadcastTxExpiredAnchorRetainsSignedTx is the post-signing half of the same
// wedge. A signed SOL tx persisted across a Bifrost restart re-derives its anchor from
// the stored keysign height to decide whether its blockhash really expired. When that
// derivation instead proves the ANCHOR is past MAX_PROCESSING_AGE, nothing ties the
// hash embedded in the bytes to that anchor: txs persisted before the consensus-anchor
// upgrade carry the scanner's recent hash, which can be newer and still live. So
// BroadcastTx must retain the bytes (plain error, no ErrResignRequired) whichever hash
// they carry, and must not poison either hash for other outbounds.
func (s *SolTestSuite) TestBroadcastTxExpiredAnchorRetainsSignedTx(c *C) {
	oldDelay := broadcastLookupDelay
	broadcastLookupDelay = 0
	defer func() { broadcastLookupDelay = oldDelay }()

	anchorHash := mockBlockhashForSlot(950)
	liveHash := mockBlockhashForSlot(1190) // a pre-upgrade scanner hash near the tip
	for _, tc := range []struct {
		name       string
		signedHash string
	}{
		{name: "consensus anchor hash", signedHash: anchorHash},
		{name: "pre-upgrade live hash", signedHash: liveHash},
	} {
		target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
		mock, server := newCountingSOLRPCServer(retainedHistoryScenario(target))

		e, pub, pubkeyMgr := s.newSolCacheTestClient(c, server)

		txOut := newSolCacheTestTxOut(pub)
		txOut.Height = keysignHeight
		rawTx, _, _, err := e.SignTx(txOut, txOut.Height)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		c.Assert(len(rawTx) > 0, Equals, true, Commentf("%s", tc.name))
		rawTx = withTxBlockhash(c, rawTx, anchorHash, tc.signedHash)
		got, ok := txBlockhashFromRawTx(rawTx)
		c.Assert(ok, Equals, true, Commentf("%s", tc.name))
		c.Assert(got, Equals, tc.signedHash, Commentf("%s", tc.name))

		// The RPC prunes past the target while the signed bytes sit in the store, the
		// derivation memo is lost to a restart, and the broadcast is rejected for an
		// unknown blockhash.
		mock.setScenario(strandedHistoryScenario(target))
		mock.failBroadcastBlockhash.Store(true)
		e.consensusHashMu.Lock()
		e.consensusHashes = nil
		e.consensusHashMu.Unlock()

		_, err = e.BroadcastTx(txOut, rawTx)
		c.Assert(err, NotNil, Commentf("%s", tc.name))
		c.Assert(errors.Is(err, stypes.ErrResignRequired), Equals, false, Commentf("%s", tc.name))
		c.Assert(e.isResignDeadHash(anchorHash), Equals, false, Commentf("%s", tc.name))
		c.Assert(e.isResignDeadHash(liveHash), Equals, false, Commentf("%s", tc.name))
		c.Assert(e.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, false, Commentf("%s", tc.name))

		c.Assert(pubkeyMgr.Stop(), IsNil)
		server.Close()
	}
}

// withTxBlockhash swaps the recent blockhash in signed bytes, standing in for a tx
// signed with a different hash (the signature is not re-checked by the mock RPC).
func withTxBlockhash(c *C, rawTx []byte, from, to string) []byte {
	fromBytes, err := base58.Decode(from)
	c.Assert(err, IsNil)
	toBytes, err := base58.Decode(to)
	c.Assert(err, IsNil)
	c.Assert(bytes.Count(rawTx, fromBytes), Equals, 1)
	return bytes.Replace(rawTx, fromBytes, toBytes, 1)
}

// TestSignTxProvenAnchorExpiryDrops covers the escape hatch that ends the #3344 wedge:
// once the finalized listing proves the anchor is past MAX_PROCESSING_AGE, SignTx
// returns the (nil,nil,nil,nil) no-op so the signer clears the item for a fresh-height
// reschedule instead of re-claiming the vault's single SOL signing slot every pass. The
// drop is pre-keysign, so no node is blamed for it.
func (s *SolTestSuite) TestSignTxProvenAnchorExpiryDrops(c *C) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	bridge := &mockSOLThorchainBridge{}
	vaultPubKey := types2.GetRandomEd25519PubKey()
	toAddr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	srv := strandedHistoryScenario(target).server(c)
	defer srv.Close()

	client := &SOLClient{
		bridge:             bridge,
		localPubKey:        "not-local",
		rpcClient:          newSolRPCForServer(srv),
		solScanner:         &SOLScanner{},
		tssKeyManager:      &mockSOLThorchainKeyManager{},
		signerCacheManager: newTestSignerCacheManager(c),
	}

	tx := stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        toAddr,
		VaultPubKeyEddsa: vaultPubKey,
		Coins:            common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))},
		MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(15000))},
		GasRate:          1,
		Memo:             "MIGRATE:838",
	}

	result, checkpoint, obs, err := client.SignTx(tx, keysignHeight)
	c.Assert(err, IsNil) // drop for reschedule, not a retained error
	c.Assert(result, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(obs, IsNil)
	c.Assert(bridge.postCalls, HasLen, 0)
}

// committedNonceBridge serves a vault's durable nonce as committed on THORChain (or a
// read error), on top of the legacy mock bridge. It has no GetPendingOutbounds, so the
// serialize gate fails open.
type committedNonceBridge struct {
	*mockSOLThorchainBridge
	committed thorclient.SolVaultNonce
	latest    *thorclient.SolVaultNonce // served for height<=0 when set
	err       error
}

func (b *committedNonceBridge) GetSolVaultNonce(_ string, height int64) (thorclient.SolVaultNonce, error) {
	if height <= 0 && b.latest != nil {
		return *b.latest, b.err
	}
	return b.committed, b.err
}

func (s *SolTestSuite) durableTestClient(c *C, bridge thorclient.ThorchainBridge, srv *httptest.Server) *SOLClient {
	return &SOLClient{
		bridge:             bridge,
		localPubKey:        "not-local",
		rpcClient:          newSolRPCForServer(srv),
		solScanner:         &SOLScanner{},
		tssKeyManager:      &mockSOLThorchainKeyManager{},
		signerCacheManager: newTestSignerCacheManager(c),
	}
}

func durableTestTx(c *C, vault common.PubKey) stypes.TxOutItem {
	toAddr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	return stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        toAddr,
		VaultPubKey:      vault,
		VaultPubKeyEddsa: vault,
		Coins:            common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))},
		MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(15000))},
		GasRate:          1,
		Memo:             "MIGRATE:838",
	}
}

// TestSignTxCommittedNonceReadErrorRetains: if the vault's committed durable nonce
// cannot be read, SignTx RETAINS the outbound (error -> retry next pass) and never
// falls back to a recent blockhash (invariant I: a legacy copy and a still-valid
// durable copy of the same outbound could both land). It retains even when the
// legacy anchor is proven expired, where the legacy path would have dropped it.
func (s *SolTestSuite) TestSignTxCommittedNonceReadErrorRetains(c *C) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	srv := strandedHistoryScenario(target).server(c)
	defer srv.Close()
	inner := &mockSOLThorchainBridge{}
	cl := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: inner, err: errors.New("thornode unavailable")}, srv)

	result, checkpoint, obs, err := cl.SignTx(durableTestTx(c, types2.GetRandomEd25519PubKey()), keysignHeight)
	c.Assert(err, ErrorMatches, ".*retaining outbound.*") // retained, neither signed legacy nor dropped
	c.Assert(result, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(obs, IsNil)
	c.Assert(inner.postCalls, HasLen, 0)
}

// TestSignTxCommittedNonceSignsDurable: a vault with a committed durable nonce signs
// a durable-nonce tx built from the COMMITTED value (AdvanceNonceAccount with the
// RecentBlockhashes sysvar, the committed nonce baked in place of a recent blockhash,
// the vault's own nonce account) — even when the recent-blockhash anchor is proven
// expired, where the legacy path would drop the outbound. The committed vault never
// takes the legacy path (invariant I).
func (s *SolTestSuite) TestSignTxCommittedNonceSignsDurable(c *C) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	srv := strandedHistoryScenario(target).server(c)
	defer srv.Close()

	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	nonceAcct, err := common.SolNonceAccountAddress(vaultAddr.String())
	c.Assert(err, IsNil)
	nonceAddr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	committed := thorclient.SolVaultNonce{NonceAccount: nonceAcct, Nonce: nonceAddr.String(), Slot: 1000, UpdatedHeight: 10}

	cl := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: committed}, srv)
	// Fail the keysign itself so SignTx hands back the checkpoint: the serialized
	// message it built. (Reaching the keysign at all proves the outbound was neither
	// retained nor dropped by the expired recent-blockhash anchor.)
	cl.tssKeyManager = &mockSOLThorchainKeyManager{err: errors.New("keysign failed")}
	// On a keysign error finishSign returns the checkpoint (message bytes) first.
	checkpoint, _, _, err := cl.SignTx(durableTestTx(c, vault), keysignHeight)
	c.Assert(err, ErrorMatches, ".*fail to sign tx.*")
	c.Assert(checkpoint, NotNil)

	sysvar := soltypes.SysVarRecentBlockhashesID
	c.Assert(bytes.Contains(checkpoint, sysvar[:]), Equals, true) // AdvanceNonceAccount present
	acct := soltypes.MustPublicKeyFromString(nonceAcct)
	c.Assert(bytes.Contains(checkpoint, acct[:]), Equals, true) // the vault's own nonce account
	baked := soltypes.MustPublicKeyFromString(committed.Nonce)
	c.Assert(bytes.Contains(checkpoint, baked[:]), Equals, true) // committed nonce baked in
}

// TestSignTxCommittedNonceForeignAccountRetains: a committed record naming an
// account other than the vault's seed-derived one is rejected (defence in depth
// behind THORChain's commit check) and the outbound retained, not signed legacy.
func (s *SolTestSuite) TestSignTxCommittedNonceForeignAccountRetains(c *C) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	srv := strandedHistoryScenario(target).server(c)
	defer srv.Close()
	foreign, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	committed := thorclient.SolVaultNonce{NonceAccount: foreign.String(), Nonce: foreign.String(), Slot: 1000}
	cl := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: committed}, srv)
	result, _, _, err := cl.SignTx(durableTestTx(c, types2.GetRandomEd25519PubKey()), keysignHeight)
	c.Assert(err, ErrorMatches, ".*invalid committed durable nonce.*")
	c.Assert(result, IsNil)
}

// durableFixture is a vault with a committed nonce and a matching durable outbound.
type durableFixture struct {
	vault     common.PubKey
	nonceAcct string
	committed thorclient.SolVaultNonce
}

func newDurableFixture(c *C) durableFixture {
	vault := types2.GetRandomEd25519PubKey()
	addr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	acct, err := common.SolNonceAccountAddress(addr.String())
	c.Assert(err, IsNil)
	nonce, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	return durableFixture{
		vault: vault, nonceAcct: acct,
		committed: thorclient.SolVaultNonce{NonceAccount: acct, Nonce: nonce.String(), Slot: 1000, UpdatedHeight: keysignHeight},
	}
}

// TestSignTxDropsWhenCommittedNonceAdvanced (B5): once the vault's committed nonce
// advanced after the outbound's keysign height, the only nonce this outbound can be
// signed with there is consumed, so SignTx DROPS it for reschedule (nil,nil,nil,nil)
// without re-signing — never retrying dead bytes, never wedging the vault.
func (s *SolTestSuite) TestSignTxDropsWhenCommittedNonceAdvanced(c *C) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	srv := strandedHistoryScenario(target).server(c)
	defer srv.Close()
	f := newDurableFixture(c)
	advanced := f.committed
	advanced.Nonce = f.nonceAcct // any different value
	advanced.UpdatedHeight = keysignHeight + 1

	cl := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: f.committed, latest: &advanced}, srv)
	cl.tssKeyManager = &mockSOLThorchainKeyManager{err: errors.New("keysign must not be reached")}
	result, checkpoint, obs, err := cl.SignTx(durableTestTx(c, f.vault), keysignHeight)
	c.Assert(err, IsNil) // a drop, not an error and not a keysign
	c.Assert(result, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(obs, IsNil)

	// Not advanced past the keysign height (equal counts as not advanced): signs.
	same := f.committed
	cl2 := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: f.committed, latest: &same}, srv)
	cl2.tssKeyManager = &mockSOLThorchainKeyManager{err: errors.New("keysign reached")}
	_, _, _, err = cl2.SignTx(durableTestTx(c, f.vault), keysignHeight)
	c.Assert(err, ErrorMatches, ".*keysign reached.*")
}

// durableRawTx is a serialized durable-nonce tx carrying nonce, as a stored signed
// copy would be (the mock RPC does not verify the signature).
func durableRawTx(c *C, f durableFixture) []byte {
	addr, err := f.vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	vaultPK, err := soltypes.PublicKeyFromString(addr.String())
	c.Assert(err, IsNil)
	dest, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	destPK, err := soltypes.PublicKeyFromString(dest.String())
	c.Assert(err, IsNil)
	tx := soltypes.NewNonceTransferTransaction(vaultPK, destPK, 100, "MIGRATE:838", f.committed.Nonce,
		soltypes.MustPublicKeyFromString(f.nonceAcct), vaultPK)
	tx.Signatures = []soltypes.Signature{make(soltypes.Signature, 64)}
	raw, err := tx.Serialize()
	c.Assert(err, IsNil)
	return raw
}

// TestBroadcastTxDeadDurableResigns (B5): a stored durable tx rejected with
// "Blockhash not found" whose committed nonce has since advanced is dead; BroadcastTx
// returns ErrResignRequired so the signer re-enters SignTx, which drops it. With the
// committed nonce NOT advanced, the bytes are retained (plain error) — the recent-
// blockhash expiry logic cannot confirm a nonce, so it must not re-sign it.
func (s *SolTestSuite) TestBroadcastTxDeadDurableResigns(c *C) {
	oldDelay := broadcastLookupDelay
	broadcastLookupDelay = 0
	defer func() { broadcastLookupDelay = oldDelay }()
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds

	for _, tc := range []struct {
		name       string
		advanced   bool
		wantResign bool
	}{
		{name: "nonce consumed", advanced: true, wantResign: true},
		{name: "nonce still current", advanced: false, wantResign: false},
	} {
		mock, server := newCountingSOLRPCServer(strandedHistoryScenario(target))
		mock.failBroadcastBlockhash.Store(true)
		f := newDurableFixture(c)
		latest := f.committed
		if tc.advanced {
			latest.Nonce = f.nonceAcct
			latest.UpdatedHeight = keysignHeight + 1
		}
		cl := s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: f.committed, latest: &latest}, server)
		txOut := durableTestTx(c, f.vault)
		txOut.Height = keysignHeight

		_, err := cl.BroadcastTx(txOut, durableRawTx(c, f))
		c.Assert(err, NotNil, Commentf("%s", tc.name))
		c.Assert(errors.Is(err, stypes.ErrResignRequired), Equals, tc.wantResign, Commentf("%s", tc.name))
		server.Close()
	}
}
