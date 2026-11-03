package signer

import (
	"fmt"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type expiryChainClient struct {
	MockChainClient
	err error
}

func (b *expiryChainClient) GetChain() common.Chain { return common.ZECChain }

func (b *expiryChainClient) SignTx(tx types.TxOutItem, _ int64) ([]byte, []byte, *types.TxInItem, error) {
	b.signCount++
	return nil, tx.Checkpoint, nil, b.err
}

func (b *expiryChainClient) BroadcastTx(types.TxOutItem, []byte) (string, error) {
	b.broadcastCount++
	return "", b.err
}

func (s *SignCoverageSuite) TestProcessTransaction_ExpiredAttempts(c *C) {
	for _, signed := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			vaultPubKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
			c.Assert(err, IsNil)
			bridge := &mockSignBridge{
				blockHeight: 100,
				constants: map[string]int64{
					constants.SigningTransactionPeriod.String(): 300,
					constants.ChurnInterval.String():            43200,
				},
				mimirs: map[string]int64{"MAXOUTBOUNDATTEMPTS": 10},
				vault:  ttypes.Vault{Status: ttypes.VaultStatus_ActiveVault},
			}
			cc := &expiryChainClient{err: fmt.Errorf("tip lookup unavailable")}
			if expired {
				cc.err = fmt.Errorf("ZEC: %w", chainclienttypes.ErrTxExpired)
			}
			storage, err := NewSignerStore("", config.LevelDBOptions{}, "")
			c.Assert(err, IsNil)
			sign := &Signer{
				logger:            log.With().Str("module", "test").Logger(),
				cfg:               config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true}},
				thorchainBridge:   bridge,
				constantsProvider: NewConstantsProvider(bridge),
				chains:            map[common.Chain]chainclients.ChainClient{common.ZECChain: cc},
				pubkeyMgr:         pubkeymanager.NewMockPoolAddressValidator(),
				m:                 GetMetricForTest(c),
				storage:           storage,
				localPubKeyECDSA:  vaultPubKey,
			}
			item := TxOutStoreItem{
				TxOutItem: types.TxOutItem{Chain: common.ZECChain, VaultPubKey: vaultPubKey, ToAddress: "zec-destination", Memo: "MIGRATE:90"},
				Height:    90, Round7Retry: true, Checkpoint: []byte("fixed-checkpoint"),
			}
			if signed {
				item.SignedTx = []byte("fixed-signed-payload")
				item.Observation = &types.TxInItem{Tx: "expired-observation"}
			}
			c.Assert(storage.Set(item), IsNil)
			// An expired saved observation must never reach auto-observe (the
			// observer is deliberately nil). RPC failures must retain the attempt.
			sign.processTransaction(item)
			items := storage.List()
			if expired {
				c.Assert(items, HasLen, 0)
			} else {
				c.Assert(items, HasLen, 1)
				c.Assert(items[0].Checkpoint, DeepEquals, item.Checkpoint)
				c.Assert(items[0].SignedTx, DeepEquals, item.SignedTx)
				c.Assert(items[0].Round7Retry, Equals, true)
			}
			c.Assert(cc.signCount == 0, Equals, signed)
			c.Assert(cc.broadcastCount == 1, Equals, signed)
			c.Assert(storage.Close(), IsNil)
		}
	}
}
