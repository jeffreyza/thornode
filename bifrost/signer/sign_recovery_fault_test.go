package signer

import (
	"bytes"
	"errors"
	"fmt"

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

// recoveryStore fails one selected write while keeping the real LevelDB store
// available for a close/reopen boundary.
type recoveryStore struct {
	SignerStorage
	failNext bool
	passN    int // writes allowed through before failNext applies
}

func (s *recoveryStore) Set(item TxOutStoreItem) error {
	if s.failNext && s.passN > 0 {
		s.passN--
		return s.SignerStorage.Set(item)
	}
	if s.failNext {
		s.failNext = false
		return errors.New("injected LevelDB write failure")
	}
	return s.SignerStorage.Set(item)
}

// recoveryChain models a chain which accepts a payload once, but loses the
// acknowledgement of the first RPC call. Repeating identical bytes is safe;
// signing new bytes could make a second economic payment.
type recoveryChain struct {
	MockChainClient
	signs     int
	requests  [][]byte
	payments  map[string]bool
	lostFirst bool
}

// SignTx returns a nonce checkpoint on success, as EVMClient.SignTx does.
func (c *recoveryChain) SignTx(types.TxOutItem, int64) ([]byte, []byte, *types.TxInItem, error) {
	c.signs++
	return []byte(fmt.Sprintf("nonce-and-payload-%d", c.signs)), []byte("nonce-7"), nil, nil
}

func (c *recoveryChain) BroadcastTx(_ types.TxOutItem, payload []byte) (string, error) {
	c.requests = append(c.requests, bytes.Clone(payload))
	c.payments[string(payload)] = true
	if !c.lostFirst {
		c.lostFirst = true
		return "", errors.New("injected lost RPC acknowledgement")
	}
	return "accepted", nil
}

func (s *SignCoverageSuite) TestRecoverySignedPayloadSurvivesAckLossAndRestart(c *C) {
	path := c.MkDir()
	store, err := NewSignerStore(path, config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	vault, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	item := NewTxOutStoreItem(90, types.TxOutItem{
		Chain: common.ETHChain, VaultPubKey: vault,
		ToAddress: "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		Memo:      "OUT:recovery",
	}, 0)
	c.Assert(store.Set(item), IsNil)
	chain := &recoveryChain{payments: make(map[string]bool)}
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300},
		mimirs:      map[string]int64{},
	}
	signer := &Signer{
		logger: log.Logger, storage: store, thorchainBridge: bridge,
		constantsProvider: NewConstantsProvider(bridge),
		chains:            map[common.Chain]chainclients.ChainClient{common.ETHChain: chain},
		pubkeyMgr:         pubkeymanager.NewMockPoolAddressValidator(),
		m:                 GetMetricForTest(c), localPubKeyECDSA: vault,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}

	// An unpersisted signature must never reach RPC, but the signed result and
	// its checkpoint are written again before another attempt may re-sign.
	fault := &recoveryStore{SignerStorage: store, failNext: true}
	signer.storage = fault
	_, _, err = signer.signAndBroadcast(item)
	c.Assert(err, ErrorMatches, "persist signed tx before broadcast:.*injected LevelDB write failure")
	c.Assert(chain.requests, HasLen, 0)
	stored, err := store.Get(item.Key())
	c.Assert(err, IsNil)
	c.Assert(stored.SignedTx, HasLen, 0)
	fault.failNext = true
	signer.processTransaction(item)
	c.Assert(chain.signs, Equals, 2)
	c.Assert(chain.requests, HasLen, 0)
	stored, err = store.Get(item.Key())
	c.Assert(err, IsNil)
	c.Assert(stored.SignedTx, DeepEquals, []byte("nonce-and-payload-2"))
	c.Assert(stored.Checkpoint, DeepEquals, []byte("nonce-7"))

	// The next pass rebroadcasts the recovered bytes; its ambiguous RPC result
	// leaves them durable.
	signer.storage = store
	_, _, err = signer.signAndBroadcast(stored)
	c.Assert(err, ErrorMatches, "injected lost RPC acknowledgement")
	c.Assert(chain.signs, Equals, 2)
	c.Assert(chain.requests, HasLen, 1)
	c.Assert(chain.requests[0], DeepEquals, []byte("nonce-and-payload-2"))
	c.Assert(chain.payments, HasLen, 1)
	c.Assert(store.Close(), IsNil)
	store, err = NewSignerStore(path, config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer func() { c.Assert(store.Close(), IsNil) }()
	signer.storage = store
	stored, err = store.Get(item.Key())
	c.Assert(err, IsNil)
	c.Assert(stored.SignedTx, DeepEquals, chain.requests[0])
	// Churn the vault before the retry. The real pipeline must finish the
	// pending signed attempt and remove it after the RPC acknowledgement.
	bridge.vault = ttypes.Vault{Status: ttypes.VaultStatus_RetiringVault}
	pipeline, err := newPipeline(1)
	c.Assert(err, IsNil)
	pipeline.SpawnSignings(signer, bridge)
	pipeline.Wait()
	c.Assert(chain.signs, Equals, 2) // only the two attempts whose first write failed
	c.Assert(chain.requests, HasLen, 2)
	c.Assert(chain.requests[1], DeepEquals, chain.requests[0])
	c.Assert(chain.payments, HasLen, 1)
	c.Assert(store.List(), HasLen, 0)
}

func (s *SignCoverageSuite) TestRecoveryGasRefreshWriteFailureStopsBeforeSigning(c *C) {
	store, err := NewSignerStore(c.MkDir(), config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer func() { c.Assert(store.Close(), IsNil) }()
	vault, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	coin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1000))
	address := common.Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	oldGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10))}
	newGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(20))}
	item := NewTxOutStoreItem(90, types.TxOutItem{
		Chain: common.ETHChain, VaultPubKey: vault, ToAddress: address,
		Coins: common.Coins{coin}, Memo: "OUT:recovery-gas", Height: 90,
		MaxGas: oldGas, GasRate: 10,
	}, 0)
	c.Assert(store.Set(item), IsNil)
	item, err = store.Get(item.Key())
	c.Assert(err, IsNil)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300},
		keysign: types.TxOut{Height: 90, TxArray: []types.TxArrayItem{{
			Chain: common.ETHChain, VaultPubKey: vault, ToAddress: address,
			Coin: coin, Memo: item.TxOutItem.Memo, MaxGas: newGas, GasRate: 20,
		}}},
	}
	chain := &recoveryChain{payments: make(map[string]bool)}
	signer := &Signer{
		logger: log.Logger, storage: &recoveryStore{SignerStorage: store, failNext: true},
		thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
		chains:    map[common.Chain]chainclients.ChainClient{common.ETHChain: chain},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(),
		m:         GetMetricForTest(c), localPubKeyECDSA: vault,
	}
	_, _, err = signer.signAndBroadcast(item)
	c.Assert(err, ErrorMatches, "persist refreshed txout gas before signing:.*injected LevelDB write failure")
	c.Assert(chain.signs, Equals, 0)
	c.Assert(chain.requests, HasLen, 0)
	stored, err := store.Get(item.Key())
	c.Assert(err, IsNil)
	c.Assert(stored.TxOutItem.GasRate, Equals, int64(10))
}

func (s *SignCoverageSuite) TestRecoveryUnsavedSignedTxKeepsRefreshedGas(c *C) {
	store, err := NewSignerStore(c.MkDir(), config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer func() { c.Assert(store.Close(), IsNil) }()
	vault, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	coin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1000))
	address := common.Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	oldGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10))}
	newGas := common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(20))}
	item := NewTxOutStoreItem(90, types.TxOutItem{
		Chain: common.ETHChain, VaultPubKey: vault, ToAddress: address,
		Coins: common.Coins{coin}, Memo: "OUT:recovery-gas-unsaved", Height: 90,
		MaxGas: oldGas, GasRate: 10,
	}, 0)
	c.Assert(store.Set(item), IsNil)
	item, err = store.Get(item.Key())
	c.Assert(err, IsNil)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300},
		mimirs:      map[string]int64{},
		keysign: types.TxOut{Height: 90, TxArray: []types.TxArrayItem{{
			Chain: common.ETHChain, VaultPubKey: vault, ToAddress: address,
			Coin: coin, Memo: item.TxOutItem.Memo, MaxGas: newGas, GasRate: 20,
		}}},
	}
	chain := &recoveryChain{payments: make(map[string]bool)}
	// The gas-refresh write succeeds, the signed-payload write fails, and the
	// recovery write in processTransaction succeeds.
	signer := &Signer{
		logger: log.Logger, storage: &recoveryStore{SignerStorage: store, failNext: true, passN: 1},
		thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
		chains:    map[common.Chain]chainclients.ChainClient{common.ETHChain: chain},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(),
		m:         GetMetricForTest(c), localPubKeyECDSA: vault,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}
	signer.processTransaction(item)
	c.Assert(chain.signs, Equals, 1)
	c.Assert(chain.requests, HasLen, 0)
	stored, err := store.Get(item.Key())
	c.Assert(err, IsNil)
	c.Assert(stored.SignedTx, DeepEquals, []byte("nonce-and-payload-1"))
	c.Assert(stored.Checkpoint, DeepEquals, []byte("nonce-7"))
	// The persisted description must match the gas the payload was signed with.
	c.Assert(stored.TxOutItem.GasRate, Equals, int64(20))
	c.Assert(stored.TxOutItem.MaxGas, DeepEquals, newGas)
}
