package core

import (
	"errors"
	rand "math/rand/v2"
	"reflect"
	"testing"
	"unsafe"

	sdkmath "cosmossdk.io/math"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestPackage runs check.v1 suites for this package.
func TestPackage(t *testing.T) { TestingT(t) }

type SwapBalanceSuite struct{}

var _ = Suite(&SwapBalanceSuite{})

func TestSwapSpendableBalanceLeavesGasReserve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		asset   common.Asset
		balance sdkmath.Uint
		want    sdkmath.Uint
	}{
		{
			name:    "non gas asset can use full balance",
			asset:   common.AVAXAsset.GetSyntheticAsset(), // force a non-gas asset? no, use token below
			balance: sdkmath.NewUint(12345),
			want:    sdkmath.NewUint(12345),
		},
		{
			name:    "sol leaves five percent reserve",
			asset:   common.SOLAsset,
			balance: sdkmath.NewUint(1_000_000_000),
			want:    sdkmath.NewUint(950_000_000),
		},
		{
			name:    "xrp leaves at least base reserve",
			asset:   common.XRPAsset,
			balance: sdkmath.NewUint(2 * common.One),
			want:    sdkmath.NewUint(common.One),
		},
		{
			name:    "utxo gas asset leaves dust reserve only",
			asset:   common.BCHAsset,
			balance: sdkmath.NewUint(10 * common.One),
			want:    sdkmath.NewUint(10 * common.One).Sub(common.BCHChain.DustThreshold()),
		},
		{
			name:    "gas asset below reserve is unusable",
			asset:   common.XRPAsset,
			balance: sdkmath.NewUint(common.One),
			want:    sdkmath.ZeroUint(),
		},
	}

	token, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse token asset: %v", err)
	}
	tests[0].asset = token

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := swapSpendableBalance(tc.asset, tc.balance)
			if !got.Equal(tc.want) {
				t.Fatalf("expected spendable balance %s, got %s", tc.want, got)
			}
		})
	}
}

func TestSwapAmountForBalanceCapsToSpendableBalance(t *testing.T) {
	t.Parallel()

	got := swapAmountForBalance(common.SOLAsset, sdkmath.NewUint(500_000_000), sdkmath.NewUint(100_000_000))
	want := sdkmath.NewUint(95_000_000)
	if !got.Equal(want) {
		t.Fatalf("expected capped swap amount %s, got %s", want, got)
	}

	got = swapAmountForBalance(common.BTCAsset, sdkmath.NewUint(500_000), sdkmath.NewUint(2_000_000))
	want = sdkmath.NewUint(500_000)
	if !got.Equal(want) {
		t.Fatalf("expected target swap amount %s, got %s", want, got)
	}
}

func TestAcquireUserForAmountPrefersHighestSpendableBalance(t *testing.T) {
	t.Parallel()

	from, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse token asset: %v", err)
	}

	actor := newSwapBalanceTestActor(from, common.BTCAsset, 1, 2)

	poorer := newTestSwapUser(t, "poorer", from, sdkmath.NewUint(120), common.BTCAsset, sdkmath.ZeroUint())
	richer := newTestSwapUser(t, "richer", from, sdkmath.NewUint(220), common.BTCAsset, sdkmath.ZeroUint())

	result := actor.acquireUserForAmount(&simtypes.OpConfig{Users: []*simtypes.User{poorer, richer}}, sdkmath.NewUint(100), true)
	if !result.Continue || result.Error != nil || result.Finish {
		t.Fatalf("expected successful acquisition, got %+v", result)
	}
	t.Cleanup(func() {
		if actor.user != nil {
			actor.user.Release()
		}
	})

	if actor.user != richer {
		t.Fatalf("expected richest user to be selected")
	}
	if !actor.fromBalance.Equal(sdkmath.NewUint(220)) {
		t.Fatalf("expected selected from balance 220, got %s", actor.fromBalance)
	}
	if !actor.swapAmount.Equal(sdkmath.NewUint(100)) {
		t.Fatalf("expected swap amount 100, got %s", actor.swapAmount)
	}
	if poorer.IsLocked() {
		t.Fatalf("expected unselected user to be released")
	}
}

func TestAcquireUserForAmountWaitsForStrictQuotedAmount(t *testing.T) {
	t.Parallel()

	from, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse token asset: %v", err)
	}

	actor := newSwapBalanceTestActor(from, common.BTCAsset, 3, 4)

	first := newTestSwapUser(t, "first", from, sdkmath.NewUint(120), common.BTCAsset, sdkmath.ZeroUint())
	second := newTestSwapUser(t, "second", from, sdkmath.NewUint(160), common.BTCAsset, sdkmath.ZeroUint())
	if !second.Acquire() {
		t.Fatal("expected to lock second user")
	}
	t.Cleanup(second.Release)

	result := actor.acquireUserForAmount(&simtypes.OpConfig{Users: []*simtypes.User{first, second}}, sdkmath.NewUint(150), false)
	if result.Continue || result.Error != nil || result.Finish {
		t.Fatalf("expected pending acquisition, got %+v", result)
	}
	if actor.user != nil {
		t.Fatalf("expected no user to be selected")
	}
	if first.IsLocked() {
		t.Fatalf("expected insufficient unlocked user to be released while waiting")
	}
	if !second.IsLocked() {
		t.Fatalf("expected externally locked user to remain locked")
	}
}

func TestAcquireUserForAmountSkipsImpossibleStrictQuotedAmount(t *testing.T) {
	t.Parallel()

	from, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse token asset: %v", err)
	}

	actor := newSwapBalanceTestActor(from, common.BTCAsset, 7, 8)

	first := newTestSwapUser(t, "first", from, sdkmath.NewUint(120), common.BTCAsset, sdkmath.ZeroUint())
	second := newTestSwapUser(t, "second", from, sdkmath.NewUint(140), common.BTCAsset, sdkmath.ZeroUint())

	result := actor.acquireUserForAmount(&simtypes.OpConfig{Users: []*simtypes.User{first, second}}, sdkmath.NewUint(150), false)
	if result.Continue || result.Error != nil || !result.Finish {
		t.Fatalf("expected impossible acquisition to finish, got %+v", result)
	}
	if actor.user != nil {
		t.Fatalf("expected no user to be selected")
	}
	if first.IsLocked() || second.IsLocked() {
		t.Fatalf("expected all users to be released")
	}
}

func TestAcquireUserForAmountSkipsImpossibleStrictQuotedAmountWithLockedUsers(t *testing.T) {
	t.Parallel()

	from, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse token asset: %v", err)
	}

	actor := newSwapBalanceTestActor(from, common.BTCAsset, 9, 10)

	first := newTestSwapUser(t, "first", from, sdkmath.NewUint(120), common.BTCAsset, sdkmath.ZeroUint())
	second := newTestSwapUser(t, "second", from, sdkmath.NewUint(140), common.BTCAsset, sdkmath.ZeroUint())
	if !second.Acquire() {
		t.Fatal("expected to lock second user")
	}
	t.Cleanup(second.Release)

	result := actor.acquireUserForAmount(&simtypes.OpConfig{Users: []*simtypes.User{first, second}}, sdkmath.NewUint(150), false)
	if result.Continue || result.Error != nil || !result.Finish {
		t.Fatalf("expected impossible acquisition to finish even with locked insufficient users, got %+v", result)
	}
	if actor.user != nil {
		t.Fatalf("expected no user to be selected")
	}
	if first.IsLocked() {
		t.Fatalf("expected unlocked insufficient user to be released")
	}
	if !second.IsLocked() {
		t.Fatalf("expected externally locked user to remain locked")
	}
}

// newSwapBalanceTestActor creates a SwapActor with deterministic RNG for
// balance-selection unit tests.
func newSwapBalanceTestActor(from, to common.Asset, seed1, seed2 uint64) *SwapActor {
	//nolint:gosec // deterministic simulation test RNG, not security-sensitive randomness.
	rng := rand.New(rand.NewPCG(seed1, seed2))
	return &SwapActor{
		Actor: *simtypes.NewActor("swap-balance-test", rng),
		from:  from,
		to:    to,
	}
}

func TestAcquireUserForAmountTracksSpendableGasBalance(t *testing.T) {
	t.Parallel()

	actor := newSwapBalanceTestActor(common.SOLAsset, common.BTCAsset, 5, 6)

	user := newTestSwapUser(t, "sol-user", common.SOLAsset, sdkmath.NewUint(100_000_000), common.BTCAsset, sdkmath.ZeroUint())

	result := actor.acquireUserForAmount(&simtypes.OpConfig{Users: []*simtypes.User{user}}, sdkmath.NewUint(80_000_000), true)
	if !result.Continue || result.Error != nil || result.Finish {
		t.Fatalf("expected successful acquisition, got %+v", result)
	}
	t.Cleanup(func() {
		if actor.user != nil {
			actor.user.Release()
		}
	})

	wantSpendable := sdkmath.NewUint(95_000_000)
	if !actor.fromBalance.Equal(wantSpendable) {
		t.Fatalf("expected spendable from balance %s, got %s", wantSpendable, actor.fromBalance)
	}

	err := actor.updateSwapAmount(sdkmath.NewUint(96_000_000), "recommended minimum inbound amount")
	if err == nil {
		t.Fatal("expected quoted amount above spendable balance to fail")
	}
}

// TestFinishAfterPostSelectionFundingErrorReleasesResources verifies a late
// funding failure releases both the selected user and the XMR slot.
func (s *SwapBalanceSuite) TestFinishAfterPostSelectionFundingErrorReleasesResources(c *C) {
	actor := newSwapBalanceTestActor(common.ZECAsset, common.XMRAsset, 9, 10)
	user := newTestSwapUserC(c, "selected", common.ZECAsset, sdkmath.NewUint(100), common.XMRAsset, sdkmath.ZeroUint())
	c.Assert(user.Acquire(), Equals, true)
	actor.user = user

	select {
	case xmrSwapSlot <- struct{}{}:
		actor.xmrSwapSlotAcquired = true
	default:
		c.Fatal("XMR swap slot was already held")
	}
	defer func() {
		if actor.xmrSwapSlotAcquired {
			actor.releaseXMRSwapSlot()
		}
		if actor.user != nil {
			actor.releaseUserSelection()
		}
	}()

	err := errors.New("fail to get unspent UTXO: insufficient balance: 0.000000 < 4.012694")
	c.Assert(isPostSelectionFundingError(err), Equals, true)

	result := actor.finishAfterPostSelectionFundingError(err)
	c.Assert(result.Finish, Equals, true)
	c.Assert(result.Continue, Equals, false)
	c.Assert(result.Error, IsNil)
	c.Assert(actor.user, IsNil)
	c.Assert(user.IsLocked(), Equals, false)
	c.Assert(actor.xmrSwapSlotAcquired, Equals, false)

	select {
	case <-xmrSwapSlot:
		c.Fatal("expected XMR swap slot channel to be empty")
	default:
	}
}

func TestOutboundAmountPlusGasPrefersActualGas(t *testing.T) {
	t.Parallel()

	out := openapi.Tx{
		Coins: []openapi.Coin{{
			Asset:  common.XMRAsset.String(),
			Amount: "81593453",
		}},
		Gas: []openapi.Coin{{
			Asset:  common.XMRAsset.String(),
			Amount: "2250993",
		}},
	}
	action := openapi.TxOutItem{
		MaxGas: []openapi.Coin{{
			Asset:  common.XMRAsset.String(),
			Amount: "14897377",
		}},
	}

	got, err := outboundAmountPlusGas(out, action, common.XMRAsset)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := sdkmath.NewUint(83_844_446)
	if !got.Equal(want) {
		t.Fatalf("expected outbound amount plus gas %s, got %s", want, got)
	}
}

func TestOutboundAmountPlusGasIgnoresDifferentGasAsset(t *testing.T) {
	t.Parallel()

	out := openapi.Tx{
		Coins: []openapi.Coin{{
			Asset:  "BASE.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25",
			Amount: "9185458",
		}},
		Gas: []openapi.Coin{{
			Asset:  common.BaseETHAsset.String(),
			Amount: "10799280",
		}},
	}

	got, err := outboundAmountPlusGas(out, openapi.TxOutItem{}, common.Asset{
		Chain:  common.BASEChain,
		Symbol: common.Symbol("TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25"),
		Ticker: "TKN",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := sdkmath.NewUint(9_185_458)
	if !got.Equal(want) {
		t.Fatalf("expected token amount only %s, got %s", want, got)
	}
}

func TestSwapQuoteDriftRangeAddsRefreshBufferForNonXMR(t *testing.T) {
	t.Parallel()

	minExpected, maxExpected := swapQuoteDriftRange(
		common.AVAXAsset,
		common.ZECAsset,
		sdkmath.NewUint(5_535_278),
		sdkmath.NewUint(6_955_342),
		sdkmath.NewUint(9_257_771),
		sdkmath.NewUint(11_505_057),
	)

	if !minExpected.Equal(sdkmath.NewUint(9_142_721)) || !maxExpected.Equal(sdkmath.NewUint(11_620_107)) {
		t.Fatalf("expected buffered current range 9142721-11620107, got %s-%s", minExpected, maxExpected)
	}
}

func TestSwapQuoteDriftRangeUsesEnvelopeForXMR(t *testing.T) {
	t.Parallel()

	minExpected, maxExpected := swapQuoteDriftRange(
		common.XMRAsset,
		common.BCHAsset,
		sdkmath.NewUint(42_101_384),
		sdkmath.NewUint(51_672_580),
		sdkmath.NewUint(13_694_927),
		sdkmath.NewUint(17_061_243),
	)

	if !minExpected.Equal(sdkmath.NewUint(13_694_927)) || !maxExpected.Equal(sdkmath.NewUint(51_672_580)) {
		t.Fatalf("expected XMR drift envelope 13694927-51672580, got %s-%s", minExpected, maxExpected)
	}
}

func TestXMRSwapSlotSerializesAnyXMRPath(t *testing.T) {
	drainXMRSwapSlotForTest()
	defer drainXMRSwapSlotForTest()

	sourceXMR := &SwapActor{from: common.XMRAsset, to: common.ETHAsset}
	targetXMR := &SwapActor{from: common.BTCAsset, to: common.XMRAsset}

	if result := sourceXMR.acquireXMRSwapSlot(nil); !result.Continue || result.Finish {
		t.Fatalf("expected source XMR swap to acquire slot: %+v", result)
	}
	if result := targetXMR.acquireXMRSwapSlot(nil); result.Continue || result.Finish {
		t.Fatalf("expected target XMR swap to wait while source XMR swap holds slot: %+v", result)
	}

	sourceXMR.releaseXMRSwapSlot()
	if result := targetXMR.acquireXMRSwapSlot(nil); !result.Continue || result.Finish {
		t.Fatalf("expected target XMR swap to acquire slot after release: %+v", result)
	}
	targetXMR.releaseXMRSwapSlot()
}

func TestXMRSwapSlotIgnoresNonXMRPath(t *testing.T) {
	drainXMRSwapSlotForTest()
	defer drainXMRSwapSlotForTest()

	nonXMR := &SwapActor{from: common.BTCAsset, to: common.ETHAsset}
	if result := nonXMR.acquireXMRSwapSlot(nil); !result.Continue || result.Finish {
		t.Fatalf("expected non-XMR swap to continue without slot: %+v", result)
	}
	if nonXMR.xmrSwapSlotAcquired {
		t.Fatal("expected non-XMR swap to avoid acquiring XMR slot")
	}
}

func drainXMRSwapSlotForTest() {
	for {
		select {
		case <-xmrSwapSlot:
		default:
			return
		}
	}
}

type testSwapLiteChainClient struct {
	account common.Account
}

func (t *testSwapLiteChainClient) GetAccount(_ *common.PubKey) (*common.Account, error) {
	account := t.account
	return &account, nil
}

func (t *testSwapLiteChainClient) SignTx(simtypes.SimTx) ([]byte, error) {
	return nil, nil
}

func (t *testSwapLiteChainClient) BroadcastTx([]byte) (string, error) {
	return "", nil
}

func newTestSwapUser(t *testing.T, name string, from common.Asset, fromBalance sdkmath.Uint, to common.Asset, toBalance sdkmath.Uint) *simtypes.User {
	t.Helper()

	user := &simtypes.User{
		ChainClients: map[common.Chain]simtypes.LiteChainClient{
			from.Chain: &testSwapLiteChainClient{
				account: common.NewAccount(0, 0, common.Coins{common.NewCoin(from, fromBalance)}, true),
			},
			to.Chain: &testSwapLiteChainClient{
				account: common.NewAccount(0, 0, common.Coins{common.NewCoin(to, toBalance)}, true),
			},
		},
	}

	setTestSwapUserField(t, user, "lock", make(chan struct{}, 1))
	setTestSwapUserField(t, user, "pubkey", thorchaintypes.GetRandomPubKey())
	setTestSwapUserField(t, user, "eddsaPubkey", thorchaintypes.GetRandomEd25519PubKey())
	setTestSwapUserField(t, user, "mnemonic", name+" test mnemonic")

	return user
}

// newTestSwapUserC builds a simulation user for check.v1 suite tests.
func newTestSwapUserC(c *C, name string, from common.Asset, fromBalance sdkmath.Uint, to common.Asset, toBalance sdkmath.Uint) *simtypes.User {
	user := &simtypes.User{
		ChainClients: map[common.Chain]simtypes.LiteChainClient{
			from.Chain: &testSwapLiteChainClient{
				account: common.NewAccount(0, 0, common.Coins{common.NewCoin(from, fromBalance)}, true),
			},
			to.Chain: &testSwapLiteChainClient{
				account: common.NewAccount(0, 0, common.Coins{common.NewCoin(to, toBalance)}, true),
			},
		},
	}

	setTestSwapUserFieldC(c, user, "lock", make(chan struct{}, 1))
	setTestSwapUserFieldC(c, user, "pubkey", thorchaintypes.GetRandomPubKey())
	setTestSwapUserFieldC(c, user, "eddsaPubkey", thorchaintypes.GetRandomEd25519PubKey())
	setTestSwapUserFieldC(c, user, "mnemonic", name+" test mnemonic")

	return user
}

func setTestSwapUserField(t *testing.T, user *simtypes.User, field string, value interface{}) {
	t.Helper()

	v := reflect.ValueOf(user).Elem().FieldByName(field)
	if !v.IsValid() {
		t.Fatalf("missing user field %s", field)
	}
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

// setTestSwapUserFieldC updates private User fields for check.v1 suite tests.
func setTestSwapUserFieldC(c *C, user *simtypes.User, field string, value interface{}) {
	v := reflect.ValueOf(user).Elem().FieldByName(field)
	if !v.IsValid() {
		c.Fatalf("missing user field %s", field)
	}
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func TestXMRReadinessFailsWhenSwapCannotBeFunded(t *testing.T) {
	for _, mode := range []string{"balance", "submission"} {
		t.Run(mode, func(t *testing.T) {
			drainXMRSwapSlotForTest()
			defer drainXMRSwapSlotForTest()
			actor := newXMRPostChurnActor(rand.New(rand.NewPCG(1, 2)))
			user := newTestSwapUser(t, "poor", common.BTCAsset, sdkmath.NewUint(common.One), common.XMRAsset, sdkmath.ZeroUint())
			config := &simtypes.OpConfig{Users: []*simtypes.User{user}}
			verified := false
			actor.Ops = []simtypes.Op{
				func(c *simtypes.OpConfig) simtypes.OpResult {
					if result := actor.acquireXMRSwapSlot(c); !result.Continue {
						t.Fatal("failed to acquire XMR slot")
					}
					if mode == "balance" {
						return actor.acquireUserForAmount(c, sdkmath.NewUint(2*common.One), false)
					}
					if !user.Acquire() {
						t.Fatal("failed to acquire user")
					}
					actor.user = user
					return actor.finishAfterPostSelectionFundingError(errors.New("insufficient funds"))
				},
				func(_ *simtypes.OpConfig) simtypes.OpResult {
					verified = true
					return simtypes.OpResult{Continue: true}
				},
			}
			if err := actor.Execute(config); err == nil {
				t.Fatal("required readiness actor succeeded without a funded swap")
			}
			if verified {
				t.Fatal("verification ran after failed funding")
			}
			if actor.xmrSwapSlotAcquired || len(xmrSwapSlot) != 0 {
				t.Fatal("funding failure retained XMR slot")
			}
			if !user.Acquire() {
				t.Fatal("funding failure retained user")
			}
			user.Release()
		})
	}
}
