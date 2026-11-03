package utxo

import (
	"errors"
	"math"

	. "gopkg.in/check.v1"

	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type zecConsolidationBridge struct {
	*zecExpiryBridge
	latestHeight int64
	latestErr    error
	observedAt   func(int64) (int64, error)
}

func (b *zecConsolidationBridge) GetBlockHeight() (int64, error) {
	return b.latestHeight, b.latestErr
}

func (b *zecConsolidationBridge) GetLastObservedInHeightAtHeight(chain common.Chain, height int64) (int64, error) {
	b.requestedHeights = append(b.requestedHeights, height)
	return b.observedAt(height)
}

func consolidationObservedAt(height int64) (int64, error) {
	switch {
	case height < 1200:
		return 3499990, nil
	case height < 1238:
		return 3500000, nil
	default:
		return 3500010, nil
	}
}

func (s *ZcashSignerSuite) TestZECConsolidationAcrossTHORChainHeightUpdate(c *C) {
	other := &ZcashSignerSuite{keys: s.keys}
	other.SetUpTest(c)
	defer other.TearDownTest(c)
	first := s.newZecExpiryFixture(c)
	defer first.rpcServer.Close()
	second := other.newZecExpiryFixture(c)
	defer second.rpcServer.Close()
	first.localHeight.Store(3500011)
	second.localHeight.Store(3500012)
	s.client.bridge = &zecConsolidationBridge{first.bridge, 1237, nil, consolidationObservedAt}
	secondBridge := &zecConsolidationBridge{second.bridge, 1238, nil, consolidationObservedAt}
	other.client.bridge = secondBridge
	vault := types.Vault{PubKey: first.tx.VaultPubKey, BlockHeight: 1100}

	// Both scanners process the same ZEC block, but latest THORChain queries
	// straddle a LastChainHeight update. Compare the actual digest and policy
	// height handed to TSS through the real consolidation entry point.
	for _, client := range []*Client{s.client, other.client} {
		err := client.consolidateVaultUTXOs(vault, 1, 3500010)
		c.Assert(err, ErrorMatches, "(?s)sign consolidate txout item: .*injected signing failure.*")
	}
	c.Assert(first.signer.heights, DeepEquals, []int64{1200})
	c.Assert(second.signer.heights, DeepEquals, first.signer.heights)
	c.Assert(first.signer.digests, HasLen, 1)
	c.Assert(second.signer.digests, DeepEquals, first.signer.digests)

	// Retrying this same scanned round after another update keeps its anchor.
	secondBridge.latestHeight = 1250
	err := other.client.consolidateVaultUTXOs(vault, 1, 3500010)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(second.signer.heights[1], Equals, int64(1200))
	c.Assert(second.signer.digests[1], DeepEquals, first.signer.digests[0])

	// A slow scanner may still invoke the same round after its fixed expiry.
	// It must fail before TSS instead of deriving a fresh expiry from latest.
	second.localHeight.Store(3500040)
	err = other.client.consolidateVaultUTXOs(vault, 1, 3500010)
	c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, true)
	c.Assert(second.signer.digests, HasLen, 2)
}

func (s *ZcashSignerSuite) TestZECConsolidationHeightResolution(c *C) {
	bridge := &zecConsolidationBridge{
		zecExpiryBridge: &zecExpiryBridge{ThorchainBridge: s.bridge},
		observedAt:      consolidationObservedAt,
	}
	s.client.bridge = bridge
	for _, tc := range []struct {
		scanned, latest, vault, expected int64
	}{
		{3500010, 1200, 1100, 1200},          // Crossing is the latest visible block.
		{3500019, 1238, 1100, 1200},          // One round spans a native reporting interval.
		{3500020, 1250, 1100, 1238},          // Next round selects the next agreed crossing.
		{3500010, 1250, 1210, 1210},          // Never evaluate policy before vault creation.
		{3500010, 1210, 1210, 1210},          // Newly created vault, no search needed.
		{3500010, math.MaxInt64, 1100, 1200}, // Lookback cannot overflow.
	} {
		height, err := s.client.getZecConsolidationHeight(tc.scanned, tc.latest, tc.vault)
		c.Assert(err, IsNil, Commentf("%+v", tc))
		c.Assert(height, Equals, tc.expected, Commentf("%+v", tc))
	}
}

func (s *ZcashSignerSuite) TestZECConsolidationDefersWithoutSharedHeight(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	vault := types.Vault{PubKey: fixture.tx.VaultPubKey, BlockHeight: 1100}
	for _, tc := range []struct {
		latest       int64
		latestErr    error
		observedAt   func(int64) (int64, error)
		errorPattern string
	}{
		{1199, nil, consolidationObservedAt, ".*not yet agreed.*"},
		{0, nil, consolidationObservedAt, ".*invalid consolidation heights.*"},
		{1238, errors.New("THORChain unavailable"), consolidationObservedAt, ".*THORChain unavailable"},
		{1238, nil, func(height int64) (int64, error) {
			if height < 1238 {
				return 0, errors.New("historical state pruned")
			}
			return consolidationObservedAt(height)
		}, ".*historical state pruned"},
	} {
		s.client.bridge = &zecConsolidationBridge{fixture.bridge, tc.latest, tc.latestErr, tc.observedAt}
		err := s.client.consolidateVaultUTXOs(vault, 1, 3500010)
		c.Assert(err, ErrorMatches, tc.errorPattern)
		c.Assert(fixture.signer.digests, HasLen, 0)
	}
}
