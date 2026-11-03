package observer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

func newTerminalErrataObserver(t *testing.T) (*Observer, func()) {
	t.Helper()
	path := t.TempDir()
	storage, err := NewObserverStorage(path, config.LevelDBOptions{})
	require.NoError(t, err)
	o := &Observer{logger: zerolog.Nop(), storage: storage, lock: &sync.Mutex{}, onDeck: make(map[txInKey]*types.TxIn)}
	o.permanentErrataBroadcastBlockFn = func(common.ErrataTx) error { return nil }
	t.Cleanup(func() { require.NoError(t, o.storage.Close()) })
	return o, func() {
		require.NoError(t, o.storage.Close())
		var err error
		o.storage, err = NewObserverStorage(path, config.LevelDBOptions{})
		require.NoError(t, err)
		o.onDeck = make(map[txInKey]*types.TxIn)
		o.attestationGossip = nil
	}
}

func newTerminalErrataGossip(o *Observer, er *common.ErrataTx) (*AttestationGossip, *AttestationState[*common.ErrataTx]) {
	local := &common.Attestation{PubKey: []byte("local"), Signature: []byte("local-signature")}
	peer := &common.Attestation{PubKey: []byte("peer"), Signature: []byte("peer-signature")}
	state := &AttestationState[*common.ErrataTx]{Item: er, attestations: []attestationSentState{
		{attestation: local, sent: true}, {attestation: peer, sent: true},
	}}
	g := &AttestationGossip{
		logger: zerolog.Nop(), pubKey: local.PubKey,
		errataTxs: map[common.ErrataTx]*AttestationState[*common.ErrataTx]{*er: state},
	}
	g.SetObserverHandleErrataTxCommitted(o)
	return g, state
}

func commitTerminalErrataEvent(t *testing.T, g *AttestationGossip, state *AttestationState[*common.ErrataTx], outcome common.ErrataTxOutcome) {
	t.Helper()
	payload, err := (&common.QuorumErrataTx{
		ErrataTx: state.Item, Attestations: state.AttestationsCopy(), Outcome: outcome,
	}).Marshal()
	require.NoError(t, err)
	g.handleQuorumErrataTxCommitted(&ebifrost.EventNotification{Payload: payload})
}

func TestErrataPendingOutcomePreservesRetryAcrossRestart(t *testing.T) {
	o, reopen := newTerminalErrataObserver(t)
	er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("errata-pending")}
	tx := types.ErrataTx{Chain: er.Chain, TxID: er.Id}
	block := types.ErrataBlock{Height: 100, Txs: []types.ErrataTx{tx}}
	require.NoError(t, o.filterErrataTx(block))
	g, state := newTerminalErrataGossip(o, &er)
	before, err := o.storage.db.Get([]byte(errataTombstoneKey(er.Chain, er.Id.String())), nil)
	require.NoError(t, err)
	record, found, err := o.storage.GetPendingErrataTx(tx)
	require.NoError(t, err)
	require.True(t, found)
	for _, outcome := range []common.ErrataTxOutcome{common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, 99} {
		commitTerminalErrataEvent(t, g, state, outcome)
		for _, att := range state.attestations {
			require.False(t, att.committed)
		}
		after, readErr := o.storage.db.Get([]byte(errataTombstoneKey(er.Chain, er.Id.String())), nil)
		require.NoError(t, readErr)
		require.Equal(t, before, after)
		afterRecord, afterFound, readErr := o.storage.GetPendingErrataTx(tx)
		require.NoError(t, readErr)
		require.True(t, afterFound)
		require.Equal(t, record, afterRecord)
	}
	// The storage and observer entrypoints enforce the same pending contract.
	require.NoError(t, o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED))
	require.NoError(t, o.storage.recordLocalErrataCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED))
	require.Error(t, o.handleErrataTxCommitted(er, 99))
	reopen()
	attempts := 0
	o.errataAttestFn = func(context.Context, common.ErrataTx) error { attempts++; return nil }
	o.retryPendingErrataAt(context.Background(), time.Now())
	require.Equal(t, 1, attempts, "restart reconstructs a retry from the durable outbox")

	g, state = newTerminalErrataGossip(o, &er)
	o.storage.beforeErrataWrite = func() error { return errors.New("receipt fsync failed") }
	commitTerminalErrataEvent(t, g, state, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION)
	for _, att := range state.attestations {
		require.False(t, att.committed)
	}
	after, err := o.storage.db.Get([]byte(errataTombstoneKey(er.Chain, er.Id.String())), nil)
	require.NoError(t, err)
	require.Equal(t, before, after)
	o.storage.beforeErrataWrite = nil
	commitTerminalErrataEvent(t, g, state, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION)
	for _, att := range state.attestations {
		require.True(t, att.committed)
	}
	reopen()
	tombstone, err := o.storage.getErrataTombstone(er.Chain, er.Id.String())
	require.NoError(t, err)
	require.Equal(t, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION, tombstone.Outcome)
	require.False(t, tombstone.isPermanentlyReverted())
	require.True(t, tombstone.blocksMinedHeight(100))
	require.False(t, tombstone.blocksMinedHeight(200))
	pending, err := o.storage.GetPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
	unresolved, err := o.reconcileCommittedXMRErrata()
	require.NoError(t, err)
	require.Zero(t, unresolved, "no-op completion requires zero REST calls or sidecar work")
	// TxID-based receipts remain terminal through duplicate events and a later
	// scanner location; this does not create an economic generation.
	require.NoError(t, o.handleErrataTxCommitted(er, tombstone.Outcome))
	block.Height = 200
	require.NoError(t, o.filterErrataTx(block))
	pending, err = o.storage.GetPendingErrata()
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestErrataRevertedReceiptSurvivesQuarantineAndPersistenceFailures(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "sidecar", true: "tombstone"}[failWrite], func(t *testing.T) {
			o, reopen := newTerminalErrataObserver(t)
			er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("errata-reverted")}
			tx := types.ErrataTx{Chain: er.Chain, TxID: er.Id}
			require.NoError(t, o.filterErrataTx(types.ErrataBlock{Height: 100, Txs: []types.ErrataTx{tx}}))
			g, state := newTerminalErrataGossip(o, &er)
			calls := 0
			o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error {
				calls++
				if failWrite {
					o.storage.beforeErrataWrite = func() error { return errors.New("quarantine receipt fsync failed") }
					return nil
				}
				return errors.New("sidecar unavailable")
			}
			commitTerminalErrataEvent(t, g, state, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			require.Zero(t, calls, "committed delivery does not perform broad quarantine")
			unresolved, err := o.reconcileCommittedXMRErrata()
			require.Error(t, err)
			require.Equal(t, 1, unresolved)
			require.Equal(t, 1, calls)
			for _, att := range state.attestations {
				require.True(t, att.committed, "terminal consensus receipt already persisted")
			}
			pending, err := o.storage.GetPendingErrata()
			require.NoError(t, err)
			require.Empty(t, pending)
			tombstone, err := o.storage.getErrataTombstone(er.Chain, er.Id.String())
			require.NoError(t, err)
			require.Equal(t, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, tombstone.Outcome)
			require.Nil(t, tombstone.VoterReverted, "local completion remains deferred")
			reopen()
			o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error { calls++; return nil }
			unresolved, err = o.reconcileCommittedXMRErrata()
			require.NoError(t, err)
			require.Zero(t, unresolved)
			require.Equal(t, 2, calls)
			tombstone, err = o.storage.getErrataTombstone(er.Chain, er.Id.String())
			require.NoError(t, err)
			require.True(t, tombstone.isPermanentlyReverted())
			require.True(t, tombstone.blocksMinedHeight(200))
			require.NoError(t, o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
			require.Equal(t, 2, calls, "duplicate terminal receipt does not repeat completed quarantine")
			// A conflicting terminal result cannot rewrite a durable receipt.
			require.Error(t, o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION))
		})
	}
}

func TestErrataNonXMRUnspecifiedRetainsDeliverySemantics(t *testing.T) {
	o, _ := newTerminalErrataObserver(t)
	er := common.ErrataTx{Chain: common.BTCChain, Id: common.TxID("errata-btc")}
	require.NoError(t, o.filterErrataTx(types.ErrataBlock{Height: 100, Txs: []types.ErrataTx{{Chain: er.Chain, TxID: er.Id}}}))
	g, state := newTerminalErrataGossip(o, &er)
	commitTerminalErrataEvent(t, g, state, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
	for _, att := range state.attestations {
		require.True(t, att.committed)
	}
	tombstone, err := o.storage.getErrataTombstone(er.Chain, er.Id.String())
	require.NoError(t, err)
	require.True(t, tombstone.Committed)
	require.Equal(t, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, tombstone.Outcome)
}

// Pause a receipt update after its reads. Concurrent terminal and voter results
// must survive both writes, including outbox completion, without an Observer lock.
func TestErrataReceiptUpdatesSerializeWithTerminalOutcome(t *testing.T) {
	for _, voterResult := range []bool{false, true} {
		t.Run(fmt.Sprintf("voterResult=%t", voterResult), func(t *testing.T) {
			o, reopen := newTerminalErrataObserver(t)
			er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("errata-concurrent-receipt")}
			tx := types.ErrataTx{Chain: er.Chain, TxID: er.Id}
			require.NoError(t, o.storage.applyErrata(types.ErrataBlock{Height: 100, Txs: []types.ErrataTx{tx}}, nil))
			require.NoError(t, o.storage.recordErrataCommitted(er, 101))
			reading := make(chan struct{})
			release := make(chan struct{})
			var writes atomic.Int32
			o.storage.beforeErrataWrite = func() error {
				if writes.Add(1) == 1 {
					close(reading)
					<-release
				}
				return nil
			}
			firstDone := make(chan error, 1)
			go func() {
				if voterResult {
					firstDone <- o.storage.recordLocalErrataCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
				} else {
					firstDone <- o.storage.recordErrataCommitted(er, 102)
				}
			}()
			<-reading
			terminalDone := make(chan error, 1)
			go func() {
				if voterResult {
					terminalDone <- o.storage.recordErrataVoterResult(er, true, nil)
				} else {
					terminalDone <- o.storage.recordLocalErrataCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
				}
			}()
			// Give the second writer a chance to finish before releasing the
			// first; serialized writers instead wait for the first batch.
			var terminalErr error
			select {
			case terminalErr = <-terminalDone:
				close(release)
			case <-time.After(50 * time.Millisecond):
				close(release)
				terminalErr = <-terminalDone
			}
			require.NoError(t, <-firstDone)
			require.NoError(t, terminalErr)
			o.storage.beforeErrataWrite = nil
			reopen()
			tombstone, err := o.storage.getErrataTombstone(er.Chain, er.Id.String())
			require.NoError(t, err)
			require.Equal(t, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, tombstone.Outcome)
			require.Zero(t, tombstone.VerifyAfterHeight)
			if voterResult {
				require.NotNil(t, tombstone.VoterReverted)
				require.True(t, *tombstone.VoterReverted)
			}
			_, found, err := o.storage.GetPendingErrataTx(tx)
			require.NoError(t, err)
			require.False(t, found)
		})
	}
}
