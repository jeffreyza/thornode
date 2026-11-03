package signer

import (
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *SignCoverageSuite) TestZECTexOutboundRejection(c *C) {
	vaultKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	transparent, err := vaultKey.GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	ethAddress, err := vaultKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	p2sh := common.Address("t3cNKv7UpFVqcmRJrvFCMiuzGj9ywpKtDP5")
	if common.CurrentChainNetwork == common.MockNet {
		p2sh = "t2BeXPVjcVq4XDf7dgW4w3mK6ZjoePxy7Gb"
	}

	for _, tc := range []struct {
		name    string
		chain   common.Chain
		address common.Address
		reject  bool
	}{
		{"mainnet TEX", common.ZECChain, "tex1lc6d7rnkhh0lpdmpyxk6nycjch7rxdj0333w7k", true},
		{"mocknet TEX", common.ZECChain, "textest1xlk5w3dleprzdug6z68wtjvcw4mtqa3s9pfays", true},
		{"malformed TEX", common.ZECChain, "tex1invalid", true},
		{"transparent", common.ZECChain, transparent, false},
		{"P2SH", common.ZECChain, p2sh, false},
		{"other chain", common.ETHChain, ethAddress, false},
	} {
		for _, state := range []string{"unsigned", "signed", "checkpoint", "terminal-round"} {
			comment := Commentf("%s, %s", tc.name, state)
			item := NewTxOutStoreItem(90, types.TxOutItem{
				Chain: tc.chain, VaultPubKey: vaultKey, ToAddress: tc.address,
				Coins: common.NewCoins(common.NewCoin(tc.chain.GetGasAsset(), cosmos.NewUint(common.One))),
				Memo:  "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
			}, 0)
			switch state {
			case "signed":
				item.SignedTx = []byte("saved signed transaction")
				if tc.reject {
					item.Observation = &types.TxInItem{Tx: "saved-observation"}
				}
			case "checkpoint":
				item.Checkpoint = []byte("saved checkpoint")
			case "terminal-round":
				item.Round7Retry = true
			}
			bridge := &mockSignBridge{
				blockHeight: 100,
				constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300},
				mimirs:      map[string]int64{"MAXOUTBOUNDATTEMPTS": 10},
				vault:       ttypes.Vault{Status: ttypes.VaultStatus_ActiveVault},
			}
			chain := &MockCaptureSignChainClient{}
			store, storeErr := NewSignerStore("", config.LevelDBOptions{}, "")
			c.Assert(storeErr, IsNil)
			c.Assert(store.Set(item), IsNil)
			obs := &destinationTestObserver{}
			sign := &Signer{
				logger: log.Logger, storage: store, observer: obs,
				cfg:                 config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true}},
				thorchainBridge:     bridge,
				constantsProvider:   NewConstantsProvider(bridge),
				chains:              map[common.Chain]chainclients.ChainClient{tc.chain: chain},
				pubkeyMgr:           pubkeymanager.NewMockPoolAddressValidator(),
				m:                   GetMetricForTest(c),
				localPubKeyECDSA:    vaultKey,
				tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
			}
			sign.processTransaction(item)
			if tc.reject {
				c.Check(chain.signedTx.ToAddress, Equals, common.NoAddress, comment)
				c.Check(chain.broadcastCount, Equals, 0, comment)
			} else {
				if state != "signed" {
					c.Check(chain.signedTx.ToAddress, Equals, tc.address, comment)
				}
				c.Check(chain.broadcastCount, Equals, 1, comment)
			}
			c.Check(obs.observations.Load(), Equals, int32(0), comment)
			c.Check(bridge.broadcastCalls, Equals, 0, comment)
			c.Check(store.Has(item.Key()), Equals, false, comment)
			c.Assert(store.Close(), IsNil)
		}
	}
}
