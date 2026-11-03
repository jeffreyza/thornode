package observer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestXMRExactBlockAndReceiptFailureRetainDurableRetry(t *testing.T) {
	for _, boundary := range []string{"unavailable", "exact-write", "receipt-write"} {
		t.Run(boundary, func(t *testing.T) {
			o, reopen := newTerminalErrataObserver(t)
			er := common.ErrataTx{Chain: common.XMRChain, Id: common.TxID("boundary-" + boundary)}
			tx := types.ErrataTx{Chain: er.Chain, TxID: er.Id}
			require.NoError(t, o.filterErrataTx(types.ErrataBlock{Height: 100, Txs: []types.ErrataTx{tx}}))
			g, state := newTerminalErrataGossip(o, &er)
			key := []byte(errataTombstoneKey(er.Chain, er.Id.String()))
			before, err := o.storage.db.Get(key, nil)
			require.NoError(t, err)
			pendingBefore, found, err := o.storage.GetPendingErrataTx(tx)
			require.NoError(t, err)
			require.True(t, found)
			blocked := false
			o.permanentErrataBroadcastBlockFn = func(common.ErrataTx) error {
				if boundary == "exact-write" {
					return errors.New("exact hash fsync failed")
				}
				blocked = true
				return nil
			}
			if boundary == "unavailable" {
				o.permanentErrataBroadcastBlockFn = nil
			}
			if boundary == "receipt-write" {
				o.storage.beforeErrataWrite = func() error {
					require.True(t, blocked, "exact block must precede the receipt batch")
					return errors.New("receipt fsync failed")
				}
			}
			require.Error(t, o.handleErrataTxCommitted(er, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED))
			commitTerminalErrataEvent(t, g, state, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			for _, att := range state.attestations {
				require.False(t, att.committed, "failed critical write must not acknowledge attestation")
			}
			reopen()
			after, err := o.storage.db.Get(key, nil)
			require.NoError(t, err)
			require.Equal(t, before, after)
			pendingAfter, found, err := o.storage.GetPendingErrataTx(tx)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, pendingBefore, pendingAfter)
			o.permanentErrataBroadcastBlockFn = func(common.ErrataTx) error { blocked = true; return nil }
			g, state = newTerminalErrataGossip(o, &er)
			commitTerminalErrataEvent(t, g, state, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			require.True(t, blocked)
			for _, att := range state.attestations {
				require.True(t, att.committed)
			}
			reopen()
			pending, err := o.storage.GetPendingErrata()
			require.NoError(t, err)
			require.Empty(t, pending)
			tombstone, err := o.storage.getErrataTombstone(er.Chain, er.Id.String())
			require.NoError(t, err)
			require.True(t, tombstone.hasCommittedRevertedOutcome())
			require.Nil(t, tombstone.VoterReverted)
			// This is the shared admission API called by S11's mutation/outcome
			// guards; the sibling's guard integration is parent-owned.
			admitted, err := o.storage.admitXMRObservation(errataTestObservation(er.Id.String(), er.Chain, 300))
			require.NoError(t, err)
			require.False(t, admitted)
			o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error { return errors.New("scanner unavailable") }
			unresolved, err := o.reconcileCommittedXMRErrata()
			require.Error(t, err)
			require.Equal(t, 1, unresolved)
			reopen()
			admitted, err = o.storage.admitXMRObservation(errataTestObservation(er.Id.String(), er.Chain, 400))
			require.NoError(t, err)
			require.False(t, admitted)
			o.permanentErrataQuarantineFn = func(common.ErrataTx, int64) error { return nil }
			unresolved, err = o.reconcileCommittedXMRErrata()
			require.NoError(t, err)
			require.Zero(t, unresolved)
			reopen()
			tombstone, err = o.storage.getErrataTombstone(er.Chain, er.Id.String())
			require.NoError(t, err)
			require.True(t, tombstone.isPermanentlyReverted())
		})
	}
}
