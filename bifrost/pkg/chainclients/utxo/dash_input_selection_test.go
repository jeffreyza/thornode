package utxo

import (
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *DashSuite) TestDashInputSelectionIgnoresLocalLocks(c *C) {
	for _, tc := range []struct {
		name          string
		confirmations int64
		minimum       int64
		amount        float64
		self, vault   bool
		eligible      bool
	}{
		{name: "customer without confirmations", minimum: 1, amount: 0.1},
		{name: "confirmed customer", confirmations: 1, minimum: 1, amount: 0.1, eligible: true},
		{name: "higher confirmation minimum", confirmations: 1, minimum: 2, amount: 0.1},
		{name: "explicit zero minimum", amount: 0.1, eligible: true},
		{name: "confirmed dust", confirmations: 10, minimum: 1, amount: 0.00000001},
		{name: "self change", minimum: 1, amount: 0.1, self: true, eligible: true},
		{name: "vault transfer", minimum: 1, amount: 0.1, vault: true, eligible: true},
		{name: "self dust", minimum: 1, amount: 0.00000001, self: true, eligible: true},
		{name: "vault dust", minimum: 1, amount: 0.00000001, vault: true, eligible: true},
	} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		pubKey := ttypes.GetRandomPubKey()
		id := f.inbound
		if tc.vault {
			id = f.lockedOutbound
		}
		client.cfg.UTXO.MinUTXOConfirmations = tc.minimum
		client.cfg.UTXO.MaxUTXOsToSpend = 2
		client.cfg.UTXO.MaxMempoolAncestors = 3
		confirmedID := strings.Repeat("9", 64)
		script := newDashP2PKHScriptHex(c, f.asgard)
		// Deliberately list the newer output first. Selection must retain its
		// confirmation ordering regardless of local lock visibility.
		f.utxos = []btcjson.ListUnspentResult{
			{TxID: id, ScriptPubKey: script, Amount: tc.amount, Confirmations: tc.confirmations},
			{TxID: confirmedID, ScriptPubKey: script, Amount: 0.1, Confirmations: 20},
		}
		if tc.self {
			c.Assert(client.temporalStorage.SaveBlockMeta(1, &sharedutxo.BlockMeta{SelfTransactions: []string{id}}), IsNil)
		}
		for _, flag := range []string{"unlocked", "instantlock", "instantlock_internal", "chainlock"} {
			for _, key := range []string{"instantlock", "instantlock_internal", "chainlock"} {
				f.txs[id][key] = key == flag
			}
			outputs, err := client.getUtxoToSpend(pubKey, common.One/10, 0)
			c.Assert(err, IsNil, Commentf("%s, %s", tc.name, flag))
			want := 1
			if tc.eligible {
				want = 2
			}
			c.Assert(outputs, HasLen, want)
			c.Assert(outputs[0].TxID, Equals, confirmedID)
			if tc.eligible {
				c.Assert(outputs[1].TxID, Equals, id)
			}
			// If the extra output is needed to pay, it must pass the same policy.
			amount, err := btcutil.NewAmount(tc.amount)
			c.Assert(err, IsNil)
			_, err = client.getUtxoToSpend(pubKey, common.One/10+amount, 0)
			if tc.eligible {
				c.Assert(err, IsNil)
			} else {
				c.Assert(err, ErrorMatches, "insufficient available UTXOs:.*")
			}
		}
	}
}

func (s *DashSuite) TestDashInputSelectionKeepsMempoolLimits(c *C) {
	for _, source := range []string{"self", "vault", "zero minimum"} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		pubKey := ttypes.GetRandomPubKey()
		id := f.inbound
		client.cfg.UTXO.MinUTXOConfirmations = 1
		switch source {
		case "self":
			c.Assert(client.temporalStorage.SaveBlockMeta(1, &sharedutxo.BlockMeta{SelfTransactions: []string{id}}), IsNil)
		case "vault":
			id = f.lockedOutbound
		case "zero minimum":
			client.cfg.UTXO.MinUTXOConfirmations = 0
		}
		f.utxos = []btcjson.ListUnspentResult{{TxID: id, ScriptPubKey: newDashP2PKHScriptHex(c, f.asgard), Amount: 0.1}}
		client.cfg.UTXO.MaxMempoolAncestors = 2
		_, err := client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, ErrorMatches, "insufficient available UTXOs:.*mempool chain limit=1.*")
		client.cfg.UTXO.MaxMempoolAncestors = 3
		outputs, err := client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, IsNil)
		c.Assert(outputs, HasLen, 1)
		f.failMempoolEntry = true
		_, err = client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, ErrorMatches, "check Dash input mempool limits:.*")
		client.cfg.UTXO.MaxMempoolAncestors = 0
		outputs, err = client.getUtxoToSpend(pubKey, 1, 0)
		c.Assert(err, IsNil)
		c.Assert(outputs, HasLen, 1)
	}
}
