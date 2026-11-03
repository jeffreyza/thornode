package signer

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type preActivationZECClient struct {
	MockChainClient
	legacyCheckpoint []byte
	nextCheckpoint   []byte
	signingHeights   []int64
}

func (b *preActivationZECClient) GetChain() common.Chain { return common.ZECChain }

func (b *preActivationZECClient) SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	b.signingHeights = append(b.signingHeights, height)
	if bytes.Equal(tx.Checkpoint, b.legacyCheckpoint) {
		// The real ZEC client's pre-activation discard contract is covered by
		// TestZECExpiryAcrossSignersAndCheckpointRetry in the UTXO package.
		return nil, nil, nil, nil
	}
	return nil, b.nextCheckpoint, nil, fmt.Errorf("injected signing failure")
}

func (s *SignCoverageSuite) TestPreActivationZECCheckpointReleasesPersistedRetry(c *C) {
	for _, status := range []ttypes.VaultStatus{ttypes.VaultStatus_ActiveVault, ttypes.VaultStatus_RetiringVault} {
		pubkey, err := common.NewPubKey(pubkeymanager.MockPubkey)
		c.Assert(err, IsNil)
		bridge := &mockSignBridge{
			blockHeight: 1000,
			constants: map[string]int64{
				constants.SigningTransactionPeriod.String(): 300,
				constants.ChurnInterval.String():            43200,
			},
			mimirs: map[string]int64{"MAXOUTBOUNDATTEMPTS": 10},
			vault:  ttypes.Vault{Status: status},
		}
		legacyCheckpoint, err := json.Marshal(sharedutxo.SignCheckpoint{UnsignedTx: []byte("legacy unsigned transaction")})
		c.Assert(err, IsNil)
		nextCheckpoint, err := json.Marshal(sharedutxo.SignCheckpoint{ZecExpiryHeight: 3500040})
		c.Assert(err, IsNil)
		cc := &preActivationZECClient{legacyCheckpoint: legacyCheckpoint, nextCheckpoint: nextCheckpoint}
		path := c.MkDir()
		storage, err := NewSignerStore(path, config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		legacy := TxOutStoreItem{
			TxOutItem: types.TxOutItem{Chain: common.ZECChain, VaultPubKey: pubkey, ToAddress: "zec-destination", Memo: "MIGRATE:90"},
			Height:    90, Round7Retry: true, Checkpoint: legacyCheckpoint,
		}
		next := TxOutStoreItem{
			TxOutItem: types.TxOutItem{Chain: common.ZECChain, VaultPubKey: pubkey, ToAddress: "zec-destination", Memo: "MIGRATE:1000"},
			Height:    1000,
		}
		c.Assert(storage.Set(legacy), IsNil)
		c.Assert(storage.Set(next), IsNil)
		c.Assert(storage.Close(), IsNil)

		// Start with the actual persisted queue, including its terminal-round
		// marker. This retry is older than the normal stale-item cutoff.
		storage, err = NewSignerStore(path, config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		sign := &Signer{
			logger:            log.With().Str("module", "pre-activation-test").Logger(),
			cfg:               config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true}},
			thorchainBridge:   bridge,
			constantsProvider: NewConstantsProvider(bridge),
			chains:            map[common.Chain]chainclients.ChainClient{common.ZECChain: cc},
			pubkeyMgr:         pubkeymanager.NewMockPoolAddressValidator(),
			m:                 GetMetricForTest(c),
			storage:           storage,
			localPubKeyECDSA:  pubkey,
		}
		pipeline, err := newPipeline(1)
		c.Assert(err, IsNil)
		pipeline.SpawnSignings(sign, bridge)
		pipeline.Wait()
		c.Assert(storage.Has(legacy.Key()), Equals, false)
		c.Assert(storage.Has(next.Key()), Equals, true)
		c.Assert(cc.signingHeights, DeepEquals, []int64{90})
		c.Assert(cc.broadcastCount, Equals, 0)

		// Retirement remains effective after another restart, and the next
		// outbound keeps its own scheduled height and new checkpoint format.
		c.Assert(storage.Close(), IsNil)
		storage, err = NewSignerStore(path, config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		sign.storage = storage
		pipeline.SpawnSignings(sign, bridge)
		pipeline.Wait()
		c.Assert(cc.signingHeights, DeepEquals, []int64{90, 1000})
		c.Assert(cc.broadcastCount, Equals, 0)
		items := storage.List()
		c.Assert(items, HasLen, 1)
		c.Assert(items[0].Height, Equals, next.Height)
		c.Assert(items[0].Round7Retry, Equals, false)
		c.Assert(items[0].Checkpoint, DeepEquals, nextCheckpoint)
		c.Assert(storage.Close(), IsNil)
	}
}
