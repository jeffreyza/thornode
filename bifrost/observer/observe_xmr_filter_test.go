package observer

import (
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	lru "github.com/hashicorp/golang-lru"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func randomXMRObservedPubKey(t *testing.T) common.PubKey {
	t.Helper()

	pk, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	_, err = pk.GetAddress(common.XMRChain)
	require.NoError(t, err)
	return pk
}

func newXMRPubKeyMgr(t *testing.T, pubKeys ...common.PubKey) *pubkeymanager.PubKeyManager {
	t.Helper()

	pubKeyMgr, err := pubkeymanager.NewPubKeyManager(nil, &metrics.Metrics{})
	require.NoError(t, err)
	for _, pk := range pubKeys {
		pubKeyMgr.AddPubKey(pk, false, common.SigningAlgoEd25519)
	}
	return pubKeyMgr
}

func TestFilterObservationsXMRUsesObservedVaultPubKey(t *testing.T) {
	t.Parallel()

	poolPubKey := randomXMRObservedPubKey(t)
	poolAddress, err := poolPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	obs := &Observer{
		pubkeyMgr: newXMRPubKeyMgr(t, poolPubKey),
	}
	item := &types.TxInItem{
		Tx:     "xmr-inbound-1",
		Sender: common.XMRSeededDummyAddress('8', []byte("xmr-inbound-sender")),
		To:     poolAddress.String(),
	}

	filtered := obs.filterObservations(common.XMRChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 1)
	require.Equal(t, item.Tx, filtered[0].Tx)
	require.True(t, filtered[0].ObservedVaultPubKey.Equals(poolPubKey))
}

func TestFilterObservationsXMRPreservesScannerAttributedVault(t *testing.T) {
	t.Parallel()

	poolPubKey := randomXMRObservedPubKey(t)
	poolAddress, err := poolPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	obs := &Observer{
		pubkeyMgr: newXMRPubKeyMgr(t),
	}
	item := &types.TxInItem{
		Tx:                  "xmr-inbound-scanner-attributed",
		Sender:              common.XMRSeededDummyAddress('8', []byte("xmr-inbound-sender")),
		To:                  poolAddress.String(),
		ObservedVaultPubKey: poolPubKey,
	}

	filtered := obs.filterObservations(common.XMRChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 1)
	require.Equal(t, item.Tx, filtered[0].Tx)
	require.True(t, filtered[0].ObservedVaultPubKey.Equals(poolPubKey))
}

func TestFilterObservationsXMRSkipsSignedCache(t *testing.T) {
	t.Parallel()

	cache, err := lru.New(8)
	require.NoError(t, err)
	cache.Add("xmr-outbound-signed", nil)

	poolPubKey := randomXMRObservedPubKey(t)
	poolAddress, err := poolPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	obs := &Observer{
		signedTxOutCache: cache,
		pubkeyMgr:        newXMRPubKeyMgr(t, poolPubKey),
	}
	item := &types.TxInItem{
		Tx:     "xmr-outbound-signed",
		Sender: poolAddress.String(),
		To:     common.XMRSeededDummyAddress('8', []byte("xmr-outbound-to")),
	}

	filtered := obs.filterObservations(common.XMRChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 0)
}

func TestFilterObservationsXMRToObservedTx(t *testing.T) {
	t.Parallel()

	poolPubKey := randomXMRObservedPubKey(t)
	poolAddress, err := poolPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	obs := &Observer{
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
		pubkeyMgr:           newXMRPubKeyMgr(t, poolPubKey),
	}
	item := &types.TxInItem{
		BlockHeight: 100,
		Tx:          "ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890",
		Memo:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
		Sender:      common.XMRSeededDummyAddress('8', []byte("xmr-observer-sender")),
		To:          poolAddress.String(),
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
	}

	filtered := obs.filterObservations(common.XMRChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 1)

	txIn := &types.TxIn{
		Chain:   common.XMRChain,
		TxArray: filtered,
	}
	obsTxs, invalid, err := obs.getThorchainTxIns(txIn, false, 100)
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Len(t, obsTxs, 1)
	require.True(t, obsTxs[0].ObservedPubKey.Equals(poolPubKey))
}

func TestFilterObservationsNonXMRStillRequiresPoolValidation(t *testing.T) {
	t.Parallel()

	obs := &Observer{}
	item := &types.TxInItem{
		Tx:                  "non-xmr-inbound-1",
		Sender:              "bc1qtest",
		To:                  "bc1qtestdest",
		ObservedVaultPubKey: common.PubKey("thorpub1somevault"),
	}

	filtered := obs.filterObservations(common.BTCChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 0)
}

func TestFilterObservationsXMRRequiresKnownVaultPubKey(t *testing.T) {
	t.Parallel()

	poolPubKey := randomXMRObservedPubKey(t)
	poolAddress, err := poolPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	obs := &Observer{
		pubkeyMgr: newXMRPubKeyMgr(t),
	}
	item := &types.TxInItem{
		Tx:     "xmr-inbound-unknown-vault",
		Sender: common.XMRSeededDummyAddress('8', []byte("xmr-inbound-unknown-sender")),
		To:     poolAddress.String(),
	}

	filtered := obs.filterObservations(common.XMRChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 0)
}
