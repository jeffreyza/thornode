package signer

import (
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/observer"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type readinessTestObserver struct{ ready atomic.Bool }

func (*readinessTestObserver) ObserveSigned(types.TxIn)                 {}
func (o *readinessTestObserver) IsChainClientStarted(common.Chain) bool { return o.ready.Load() }

type countingSigningTestClient struct {
	MockChainClient
	signatures atomic.Int64
	broadcasts atomic.Int64
}

func (c *countingSigningTestClient) SignTx(types.TxOutItem, int64) ([]byte, []byte, *types.TxInItem, error) {
	c.signatures.Add(1)
	return []byte("signed"), nil, nil, nil
}

func (c *countingSigningTestClient) BroadcastTx(types.TxOutItem, []byte) (string, error) {
	c.broadcasts.Add(1)
	return "signed-hash", nil
}

func (s *SignCoverageSuite) TestXMRReadinessPipelineRetainsDurableRetries(c *C) {
	path := c.MkDir()
	storage, err := NewSignerStore(path, config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer func() { c.Assert(storage.Close(), IsNil) }()
	vaultKey, _ := common.NewPubKey(pubkeymanager.MockPubkey)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants: map[string]int64{
			constants.SigningTransactionPeriod.String(): 300,
			constants.ChurnInterval.String():            43200,
		},
		mimirs: map[string]int64{},
		vault:  ttypes.Vault{PubKey: vaultKey, Status: ttypes.VaultStatus_ActiveVault},
	}
	xmrClient, btcClient := &countingSigningTestClient{}, &countingSigningTestClient{}
	obs := &readinessTestObserver{}
	sign := &Signer{
		logger: log.Logger, observer: obs, storage: storage,
		thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
		chains:    map[common.Chain]chainclients.ChainClient{common.XMRChain: xmrClient, common.BTCChain: btcClient},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), m: GetMetricForTest(c),
		localPubKeyECDSA: vaultKey, tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}
	xmr := NewTxOutStoreItem(90, types.TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultKey, ToAddress: "destination"}, 0)
	btc := NewTxOutStoreItem(90, types.TxOutItem{Chain: common.BTCChain, VaultPubKey: vaultKey, ToAddress: "destination"}, 1)
	c.Assert(storage.Set(xmr), IsNil)
	c.Assert(storage.Set(btc), IsNil)
	before, err := storage.db.Get([]byte(xmr.Key()), nil)
	c.Assert(err, IsNil)
	pipeline, err := newPipeline(2)
	c.Assert(err, IsNil)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(btcClient.signatures.Load(), Equals, int64(1))
	c.Assert(btcClient.broadcasts.Load(), Equals, int64(1))
	c.Assert(xmrClient.signatures.Load(), Equals, int64(0))
	c.Assert(xmrClient.broadcasts.Load(), Equals, int64(0))
	// Deferral performs no policy/expiry checks, even far beyond all ordinary
	// signing windows. Unknown Observer and typed nil both fail closed for XMR.
	bridge.blockHeight = 1_000_000_000
	for _, unavailable := range []signerObserver{nil, (*observer.Observer)(nil), obs} {
		sign.observer = unavailable
		for range 100 {
			pipeline.SpawnSignings(sign, bridge)
		}
		pipeline.Wait()
		after, readErr := storage.db.Get([]byte(xmr.Key()), nil)
		c.Assert(readErr, IsNil)
		c.Assert(after, DeepEquals, before)
	}
	c.Assert(xmrClient.signatures.Load(), Equals, int64(0))
	c.Assert(xmrClient.broadcasts.Load(), Equals, int64(0))
	c.Assert(storage.Close(), IsNil)
	storage, err = NewSignerStore(path, config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	sign.storage = storage
	c.Assert(storage.List(), HasLen, 1)
	// Once started, the retained unsigned item follows normal signing policy.
	bridge.blockHeight = 100
	sign.observer = obs
	obs.ready.Store(true)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(xmrClient.signatures.Load(), Equals, int64(1))
	c.Assert(xmrClient.broadcasts.Load(), Equals, int64(1))
	c.Assert(storage.List(), HasLen, 0)
	// A signed retry is also hidden while stopping/deferred, then broadcasts
	// its retained artifact without requesting another signature.
	xmr.SignedTx = []byte("old-signed-artifact")
	c.Assert(storage.Set(xmr), IsNil)
	obs.ready.Store(false)
	bridge.blockHeight = 1_000_000_000
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(storage.List(), HasLen, 1)
	c.Assert(xmrClient.broadcasts.Load(), Equals, int64(1))
	obs.ready.Store(true)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(xmrClient.signatures.Load(), Equals, int64(1))
	c.Assert(xmrClient.broadcasts.Load(), Equals, int64(2))
	c.Assert(storage.List(), HasLen, 0)
}

type scannerPolicySignBridge struct {
	mockSignBridge
	scannerReads int
}

func (b *scannerPolicySignBridge) GetMimir(key string) (int64, error) {
	if key == "HaltScannerXMR" {
		b.scannerReads++
	}
	return b.mockSignBridge.GetMimir(key)
}

func (b *scannerPolicySignBridge) GetMimirWithRef(template, ref string) (int64, error) {
	return b.GetMimir(fmt.Sprintf(template, ref))
}

type scannerHealthSigningClient struct {
	countingSigningTestClient
	healthy bool
}

func (c *scannerHealthSigningClient) IsBlockScannerHealthy() bool { return c.healthy }

func (*scannerHealthSigningClient) GetChain() common.Chain { return common.XMRChain }

func (s *SignCoverageSuite) TestXMRScannerAndSigningHaltPoliciesRemainDistinct(c *C) {
	for _, scenario := range []struct {
		name         string
		scannerHalt  int64
		signingHalt  int64
		wantPaused   bool
		wantSignings int64
	}{
		{name: "paused scanner blocks signing through health", scannerHalt: 1, wantPaused: true},
		{name: "open scanner permits ordinary signing", wantSignings: 1},
		{name: "signing halt leaves scanner open", signingHalt: 1},
	} {
		vaultKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
		c.Assert(err, IsNil)
		bridge := &scannerPolicySignBridge{mockSignBridge: mockSignBridge{
			blockHeight: 100,
			constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300},
			mimirs:      map[string]int64{"HaltScannerXMR": scenario.scannerHalt, "HaltSigningXMR": scenario.signingHalt},
		}}
		paused := blockscanner.IsChainPaused(config.BifrostBlockScannerConfiguration{ChainID: common.XMRChain}, zerolog.Nop(), bridge)
		c.Assert(paused, Equals, scenario.wantPaused, Commentf("%s", scenario.name))
		client := &scannerHealthSigningClient{healthy: !paused}
		storage, err := NewSignerStore(c.MkDir(), config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		defer func() { c.Assert(storage.Close(), IsNil) }()
		signer := &Signer{
			logger: zerolog.Nop(), storage: storage, thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
			chains:    map[common.Chain]chainclients.ChainClient{common.XMRChain: client},
			pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), localPubKeyECDSA: vaultKey, m: GetMetricForTest(c),
		}
		// Keep a normal memo-bearing outbound; this does not enable memoless XMR.
		item := NewTxOutStoreItem(90, types.TxOutItem{Chain: common.XMRChain, VaultPubKey: vaultKey, ToAddress: "destination", Memo: "MIGRATE:90"}, 0)
		reads := bridge.scannerReads
		_, _, signErr := signer.signAndBroadcast(item)
		if paused {
			c.Assert(signErr, ErrorMatches, ".*block scanner.*unhealthy.*")
		} else {
			c.Assert(signErr, IsNil)
		}
		c.Assert(client.signatures.Load(), Equals, scenario.wantSignings, Commentf("%s", scenario.name))
		c.Assert(client.broadcasts.Load(), Equals, scenario.wantSignings, Commentf("%s", scenario.name))
		c.Assert(bridge.scannerReads, Equals, reads, Commentf("outbound signing must use its own halt policy"))
	}
}
