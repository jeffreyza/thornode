package keeperv1

import (
	"bytes"
	"fmt"
	"math/big"

	storetypes "cosmossdk.io/store/types"
	"github.com/blang/semver"
	"google.golang.org/protobuf/encoding/protowire"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	keeperpkg "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperNodeAccountSuite struct{}

var _ = Suite(&KeeperNodeAccountSuite{})

func (s *KeeperNodeAccountSuite) TestNodeAccount(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(10)

	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeStandby)
	na3 := GetRandomVaultNode(NodeActive)

	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)
	c.Check(na1.ActiveBlockHeight, Equals, int64(10))
	c.Check(na2.ActiveBlockHeight, Equals, int64(0))

	count, err := k.TotalActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Check(count, Equals, 1)

	na, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na.Equals(na1), Equals, true)

	na, err = k.GetNodeAccountByPubKey(ctx, na1.PubKeySet.Secp256k1)
	c.Assert(err, IsNil)
	c.Check(na.Equals(na1), Equals, true)

	valCon := "im unique!"
	pubkeys := GetRandomPubKeySet()
	err = k.EnsureNodeKeysUnique(ctx, na1.ValidatorConsPubKey, common.EmptyPubKeySet)
	c.Assert(err, NotNil)
	err = k.EnsureNodeKeysUnique(ctx, "", pubkeys)
	c.Assert(err, NotNil)
	err = k.EnsureNodeKeysUnique(ctx, na1.ValidatorConsPubKey, pubkeys)
	c.Assert(err, NotNil)
	err = k.EnsureNodeKeysUnique(ctx, valCon, na1.PubKeySet)
	c.Assert(err, NotNil)
	err = k.EnsureNodeKeysUnique(ctx, valCon, pubkeys)
	c.Assert(err, IsNil)
	addr := GetRandomBech32Addr()
	na, err = k.GetNodeAccount(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(na.Status, Equals, NodeUnknown)
	c.Assert(na.ValidatorConsPubKey, Equals, "")
	nodeAccounts, err := k.ListValidatorsWithBond(ctx)
	c.Check(err, IsNil)
	c.Check(nodeAccounts.Len() > 0 && nodeAccounts.Len() < 3, Equals, true)
}

// getNodeAccountOracle is GetNodeAccount as it was: the default record
// decoded over through the codec.
func getNodeAccountOracle(ctx cosmos.Context, k KVStore, addr cosmos.AccAddress) (NodeAccount, error) {
	emptyPubKeySet := common.PubKeySet{
		Secp256k1: common.EmptyPubKey,
		Ed25519:   common.EmptyPubKey,
	}
	record := NewNodeAccount(addr, NodeUnknown, emptyPubKeySet, "", cosmos.ZeroUint(), "", ctx.BlockHeight())
	key := k.GetKey(prefixNodeAccount, addr.String())
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return record, nil
	}
	if err := k.cdc.Unmarshal(store.Get(key), &record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", &record, key), err)
	}
	return record, nil
}

func (s *KeeperNodeAccountSuite) TestGetNodeAccountMatchesOracle(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)
	check := func(addr cosmos.AccAddress, comment CommentInterface) {
		for pass := 0; pass < 2; pass++ { // memo miss, then hit
			wantCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
			want, wantErr := getNodeAccountOracle(wantCtx, k, addr)
			gotCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
			got, gotErr := k.GetNodeAccount(gotCtx, addr)
			c.Assert(got, DeepEquals, want, comment)
			c.Assert(gotErr == nil, Equals, wantErr == nil, comment)
			if wantErr != nil {
				c.Assert(gotErr.Error(), Equals, wantErr.Error(), comment)
			}
			c.Assert(gotCtx.GasMeter().GasConsumed(), Equals, wantCtx.GasMeter().GasConsumed(), comment)
		}
	}

	// absent: the default record
	check(GetRandomBech32Addr(), Commentf("absent"))
	// stored, including unusual and undecodable encodings
	for i, bz := range nodeAccountTestBlobs(c, k) {
		if len(bz) == 0 {
			continue // an empty value cannot be stored
		}
		addr := GetRandomBech32Addr()
		store.Set(k.GetKey(prefixNodeAccount, addr.String()), bz)
		check(addr, Commentf("blob %d", i))
	}
	na := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	check(na.NodeAddress, Commentf("set"))
}

func (s *KeeperNodeAccountSuite) TestGetMinJoinVersion(c *C) {
	type nodeInfo struct {
		status  NodeStatus
		version semver.Version
	}
	inputs := []struct {
		nodeInfoes            []nodeInfo
		expectedVersion       semver.Version
		expectedActiveVersion semver.Version
	}{
		{
			nodeInfoes: []nodeInfo{
				{
					status:  NodeActive,
					version: semver.MustParse("0.2.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.3.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.3.0"),
				},
				{
					status:  NodeStandby,
					version: semver.MustParse("0.2.0"),
				},
				{
					status:  NodeStandby,
					version: semver.MustParse("0.2.0"),
				},
			},
			expectedVersion:       semver.MustParse("0.3.0"),
			expectedActiveVersion: semver.MustParse("0.2.0"),
		},
		{
			nodeInfoes: []nodeInfo{
				{
					status:  NodeActive,
					version: semver.MustParse("0.2.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("1.3.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.3.0"),
				},
				{
					status:  NodeStandby,
					version: semver.MustParse("0.2.0"),
				},
				{
					status:  NodeStandby,
					version: semver.MustParse("0.2.0"),
				},
			},
			expectedVersion:       semver.MustParse("0.3.0"),
			expectedActiveVersion: semver.MustParse("0.2.0"),
		},
		{
			nodeInfoes: []nodeInfo{
				{
					status:  NodeActive,
					version: semver.MustParse("0.2.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("1.3.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.3.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.2.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.2.0"),
				},
			},
			expectedVersion:       semver.MustParse("0.2.0"),
			expectedActiveVersion: semver.MustParse("0.2.0"),
		},
		{
			nodeInfoes: []nodeInfo{
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0+a"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0+b"),
				},
			},
			expectedVersion:       semver.MustParse("0.79.0"),
			expectedActiveVersion: semver.MustParse("0.79.0"),
		},
		{
			nodeInfoes: []nodeInfo{
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0-c"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0-a"),
				},
				{
					status:  NodeActive,
					version: semver.MustParse("0.79.0-b"),
				},
			},
			expectedVersion:       semver.MustParse("0.79.0-b"),
			expectedActiveVersion: semver.MustParse("0.79.0-a"),
		},
	}

	for _, item := range inputs {
		ctx, k := setupKeeperForTest(c)
		for _, ni := range item.nodeInfoes {
			na1 := GetRandomValidatorNode(ni.status)
			na1.Version = ni.version.String()
			c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
		}
		c.Check(k.GetMinJoinVersion(ctx).Equals(item.expectedVersion), Equals, true, Commentf("%+v", k.GetMinJoinVersion(ctx)))
		c.Check(k.GetLowestActiveVersion(ctx).Equals(item.expectedActiveVersion), Equals, true)
	}
}

func (s *KeeperNodeAccountSuite) TestNodeAccountSlashPoints(c *C) {
	ctx, k := setupKeeperForTest(c)
	addr := GetRandomBech32Addr()

	pts, err := k.GetNodeAccountSlashPoints(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(0))

	pts = 5
	k.SetNodeAccountSlashPoints(ctx, addr, pts)
	pts, err = k.GetNodeAccountSlashPoints(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(5))

	c.Assert(k.IncNodeAccountSlashPoints(ctx, addr, 12), IsNil)
	pts, err = k.GetNodeAccountSlashPoints(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(17))

	c.Assert(k.DecNodeAccountSlashPoints(ctx, addr, 7), IsNil)
	pts, err = k.GetNodeAccountSlashPoints(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(10))
	k.ResetNodeAccountSlashPoints(ctx, GetRandomBech32Addr())
}

func (s *KeeperNodeAccountSuite) TestJail(c *C) {
	ctx, k := setupKeeperForTest(c)
	addr := GetRandomBech32Addr()

	jail, err := k.GetNodeAccountJail(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(jail.NodeAddress.Equals(addr), Equals, true)
	c.Check(jail.ReleaseHeight, Equals, int64(0))
	c.Check(jail.Reason, Equals, "")

	// ensure setting it works
	err = k.SetNodeAccountJail(ctx, addr, 50, "foo")
	c.Assert(err, IsNil)
	jail, err = k.GetNodeAccountJail(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(jail.NodeAddress.Equals(addr), Equals, true)
	c.Check(jail.ReleaseHeight, Equals, int64(50))
	c.Check(jail.Reason, Equals, "foo")

	// ensure we won't reduce sentence
	err = k.SetNodeAccountJail(ctx, addr, 20, "bar")
	c.Assert(err, IsNil)
	jail, err = k.GetNodeAccountJail(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(jail.NodeAddress.Equals(addr), Equals, true)
	c.Check(jail.ReleaseHeight, Equals, int64(50))
	c.Check(jail.Reason, Equals, "foo")

	// ensure we can update
	err = k.SetNodeAccountJail(ctx, addr, 70, "bar")
	c.Assert(err, IsNil)
	jail, err = k.GetNodeAccountJail(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(jail.NodeAddress.Equals(addr), Equals, true)
	c.Check(jail.ReleaseHeight, Equals, int64(70))
	c.Check(jail.Reason, Equals, "bar")
}

func (s *KeeperNodeAccountSuite) TestBondProviders(c *C) {
	acc := GetRandomBech32Addr()
	bp := NewBondProviders(acc)
	bp.NodeOperatorFee = cosmos.NewUint(2000)
	p := NewBondProvider(acc)
	p.Bond = cosmos.NewUint(100)
	bp.Providers = append(bp.Providers, p)
	c.Assert(bp.Providers, HasLen, 1)

	ctx, k := setupKeeperForTest(c)
	c.Assert(k.SetBondProviders(ctx, bp), IsNil)

	providers, err := k.GetBondProviders(ctx, acc)
	c.Assert(err, IsNil)
	c.Assert(providers.Providers, HasLen, 1)
}

func (s *KeeperNodeAccountSuite) TestRemoveLowBondValidatorAccounts(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(10)

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.ZeroUint()

	na2 := GetRandomValidatorNode(NodeStandby)
	na2.Bond = cosmos.NewUint(common.One)
	na2Coin := common.NewCoin(common.RuneAsset(), na2.Bond)

	// sending tokens to bond
	c.Assert(k.MintToModule(ctx, ModuleName, na2Coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(na2Coin)), IsNil)

	// adding bond providers
	bAddress, err := na2.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	bond := cosmos.NewUint(common.One / 2)
	na2BondProviders := BondProviders{
		NodeAddress:     na2.NodeAddress,
		NodeOperatorFee: cosmos.ZeroUint(),
		Providers: []BondProvider{
			{
				BondAddress: bAddress,
				Bond:        bond,
			},
			{
				BondAddress: GetRandomBech32Addr(),
				Bond:        bond,
			},
		},
	}
	c.Assert(k.SetBondProviders(ctx, na2BondProviders), IsNil)

	na3 := GetRandomVaultNode(NodeStandby)
	na3.Bond = cosmos.NewUint(10 * common.One)

	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)
	c.Assert(k.RemoveLowBondValidatorAccounts(ctx), IsNil)

	// 1st and 3rd na should be skipped
	na1Store, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na1Store.IsEmpty(), Equals, false)
	c.Check(na1Store.String(), Equals, na1.String())

	na2Store, err := k.GetNodeAccount(ctx, na2.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na2Store.IsEmpty(), Equals, true)

	// check bond providers
	baStore, err := k.GetBondProviders(ctx, bAddress)
	c.Assert(err, IsNil)
	c.Check(len(baStore.Providers), Equals, 0)
	// check bal
	for _, bps := range na2BondProviders.Providers {
		bal := k.GetBalance(ctx, bps.BondAddress)
		c.Check(bal[0].Amount.Int64(), Equals, int64(bond.Uint64()))
	}

	na3Store, err := k.GetNodeAccount(ctx, na3.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na3Store.IsEmpty(), Equals, false)
	c.Check(na3Store.String(), Equals, na3.String())
}

func (s *KeeperNodeAccountSuite) TestRemoveLowBondValidatorAccounts_PartialSendFailure(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(10)

	// Create a standby validator with bond <= 1 RUNE (eligible for removal)
	na := GetRandomValidatorNode(NodeStandby)
	na.Bond = cosmos.NewUint(common.One) // exactly 1 RUNE

	providerAddr1 := GetRandomBech32Addr()
	providerAddr2 := GetRandomBech32Addr()
	halfBond := cosmos.NewUint(common.One / 2)

	bps := BondProviders{
		NodeAddress:     na.NodeAddress,
		NodeOperatorFee: cosmos.ZeroUint(),
		Providers: []BondProvider{
			{BondAddress: providerAddr1, Bond: halfBond},
			{BondAddress: providerAddr2, Bond: halfBond},
		},
	}

	// Only fund the bond module with enough for ONE provider (half the total bond).
	// The second SendFromModuleToAccount should fail due to insufficient funds.
	fundCoin := common.NewCoin(common.RuneAsset(), halfBond)
	c.Assert(k.MintToModule(ctx, ModuleName, fundCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(fundCoin)), IsNil)

	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	c.Assert(k.SetBondProviders(ctx, bps), IsNil)
	c.Assert(k.RemoveLowBondValidatorAccounts(ctx), IsNil)

	// Node account should be PRESERVED (not deleted) since one send failed.
	naStore, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(naStore.IsEmpty(), Equals, false, Commentf("node account should be preserved on partial send failure"))

	// Bond should be reduced by only the successfully sent amount.
	c.Check(naStore.Bond.Equal(halfBond), Equals, true,
		Commentf("bond should equal unsent amount, got %s", naStore.Bond))

	// Bond providers should be PRESERVED so the remaining funds are recoverable.
	bpsStore, err := k.GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(len(bpsStore.Providers), Equals, 2,
		Commentf("bond providers should be preserved on partial send failure"))

	// The first provider should have received their funds.
	bal1 := k.GetBalance(ctx, providerAddr1)
	c.Check(bal1[0].Amount.Int64(), Equals, int64(halfBond.Uint64()),
		Commentf("first provider should have received their bond"))

	// The second provider should have received nothing.
	bal2 := k.GetBalance(ctx, providerAddr2)
	c.Check(len(bal2), Equals, 0,
		Commentf("second provider should not have received funds"))
}

func (s *KeeperNodeAccountSuite) TestRemoveLowBondValidatorAccounts_NoBondProvidersSendFailure(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(10)

	// Create a standby validator with a small non-zero bond and no bond providers.
	na := GetRandomValidatorNode(NodeStandby)
	na.Bond = cosmos.NewUint(common.One) // exactly 1 RUNE

	// Do NOT fund the bond module — SendFromModuleToAccount will fail.
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	c.Assert(k.RemoveLowBondValidatorAccounts(ctx), IsNil)

	// Node account should be PRESERVED (not deleted) since the send failed.
	naStore, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(naStore.IsEmpty(), Equals, false,
		Commentf("node account should be preserved when send fails with no bond providers"))
	c.Check(naStore.Bond.Equal(na.Bond), Equals, true,
		Commentf("bond should remain unchanged, got %s", naStore.Bond))
}

func (s *KeeperNodeAccountSuite) TestListValidatorsByStatusFilters(c *C) {
	ctx, k := setupKeeperForTest(c)
	// ListValidatorsByStatus still returns exactly the matching validators
	for _, status := range []NodeStatus{NodeActive, NodeStandby, types.NodeStatus_Disabled} {
		for i := 0; i < 3; i++ {
			c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(status)), IsNil)
		}
	}
	vault := GetRandomValidatorNode(NodeActive)
	vault.Type = NodeTypeVault
	c.Assert(k.SetNodeAccount(ctx, vault), IsNil)
	active, err := k.ListValidatorsByStatus(ctx, NodeActive)
	c.Assert(err, IsNil)
	c.Check(active, HasLen, 3)
	for _, na := range active {
		c.Check(na.Status, Equals, NodeActive)
		c.Check(na.Type, Equals, NodeTypeValidator)
	}
}

// nodeAccountTestBlobs returns well-formed node account encodings plus
// unusual and malformed ones, for decoders that must match k.cdc.Unmarshal.
func nodeAccountTestBlobs(c *C, k KVStore) [][]byte {
	vaults := make([]string, 8)
	for i := range vaults {
		vaults[i] = GetRandomPubKey().String()
	}
	var blobs [][]byte
	for i := 0; i < 50; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Version = "3.20.0"
		for j := 0; j < i%10; j++ {
			na.SignerMembership = append(na.SignerMembership, vaults[(i+j)%len(vaults)])
		}
		bz, err := k.cdc.Marshal(&na)
		c.Assert(err, IsNil)
		blobs = append(blobs, bz)
	}
	// field 9 split around other fields, repeated scalar fields (last wins),
	// an empty member, and malformed tails
	extra := protowire.AppendTag(nil, nodeAccountSignerMembershipField, protowire.BytesType)
	extra = protowire.AppendString(extra, vaults[0])
	status := protowire.AppendTag(nil, nodeAccountStatusField, protowire.VarintType)
	status = protowire.AppendVarint(status, uint64(NodeStandby))
	empty := protowire.AppendTag(nil, nodeAccountSignerMembershipField, protowire.BytesType)
	empty = protowire.AppendString(empty, "")
	wrongType := protowire.AppendTag(nil, nodeAccountSignerMembershipField, protowire.VarintType)
	wrongType = protowire.AppendVarint(wrongType, 1)
	base := blobs[5]
	blobs = append(blobs,
		append(append(append(append([]byte{}, extra...), base...), status...), extra...),
		append(append([]byte{}, base...), empty...),
		append(append([]byte{}, base...), wrongType...),
		append(append([]byte{}, base...), 0x4a, 0x05, 'a'),
		[]byte{0xff},
		nil,
	)
	return blobs
}

func (s *KeeperNodeAccountSuite) TestUnmarshalNodeAccountMatchesCodec(c *C) {
	_, k := setupKeeperForTest(c)
	for i, bz := range nodeAccountTestBlobs(c, k) {
		var want, got, gotShared NodeAccount
		wantErr := k.cdc.Unmarshal(bz, &want)
		gotErr := k.unmarshalNodeAccount(bz, &got)
		c.Assert(gotErr == nil, Equals, wantErr == nil, Commentf("blob %d", i))
		sharedErr := k.decodeNodeAccount(bytes.Clone(bz), &gotShared, true)
		c.Assert(sharedErr == nil, Equals, wantErr == nil, Commentf("blob %d", i))
		if wantErr == nil {
			c.Assert(got, DeepEquals, want, Commentf("blob %d", i))
			c.Assert(gotShared, DeepEquals, want, Commentf("blob %d", i))
		}
	}
}

func (s *KeeperNodeAccountSuite) TestActiveValidatorsCache(c *C) {
	ctx, k := setupKeeperForTest(c)
	na1 := GetRandomValidatorNode(NodeActive)
	na1.SignerMembership = []string{"a", "b"}
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	cachedCtx := keeperpkg.WithActiveValidatorsCache(ctx)

	nodes, err := k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Assert(nodes, HasLen, 1)

	// Callers own what they get back: mutating it does not reach the cache.
	nodes[0].SignerMembership[0] = "mutated"
	nodes[0].NodeAddress[0] ^= 0xff
	nodes[0].Bond.BigIntMut().SetInt64(1)
	nodes[0].Version = "9.9.9"
	nodes, err = k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Assert(nodes, HasLen, 1)
	c.Check(nodes[0].Equals(na1), Equals, true)
	c.Check(nodes[0].SignerMembership, DeepEquals, []string{"a", "b"})
	c.Check(nodes[0].Bond.Equal(na1.Bond), Equals, true)
	c.Check(nodes[0].Version, Equals, na1.Version)

	// A write through a context without the cache is not seen through it.
	na2 := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	nodes, err = k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 1)
	nodes, err = k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 2)

	// Other statuses are not cached.
	c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(NodeStandby)), IsNil)
	nodes, err = k.ListValidatorsByStatus(cachedCtx, NodeStandby)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 1)
}

func (s *KeeperNodeAccountSuite) TestActiveValidatorsCacheDisabledByWrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	na1 := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	cachedCtx := keeperpkg.WithActiveValidatorsCache(ctx)
	nodes, err := k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Assert(nodes, HasLen, 1)

	// A write in a discarded cache context still disables the shared cache,
	// and the parent then reads its own (unchanged) store.
	branch, _ := cachedCtx.CacheContext()
	na2 := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(branch, na2), IsNil)
	nodes, err = k.ListActiveValidators(branch)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 2)
	_, ok := keeperpkg.ActiveValidatorsCacheFromContext(cachedCtx).Get()
	c.Check(ok, Equals, false)
	nodes, err = k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 1)

	// Writes made later through the parent are now visible.
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	nodes, err = k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 2)

	// Deleting a node account key disables a fresh cache too.
	cachedCtx = keeperpkg.WithActiveValidatorsCache(ctx)
	nodes, err = k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Assert(nodes, HasLen, 2)
	k.DeleteKey(cachedCtx, k.GetKey(prefixNodeAccount, na2.NodeAddress.String()))
	nodes, err = k.ListActiveValidators(cachedCtx)
	c.Assert(err, IsNil)
	c.Check(nodes, HasLen, 1)
}

func (s *KeeperNodeAccountSuite) TestCountActiveValidators(c *C) {
	ctx, k := setupKeeperForTest(c)
	for _, status := range []NodeStatus{NodeStandby, NodeActive, NodeActive, types.NodeStatus_Ready, NodeActive} {
		c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(status)), IsNil)
	}
	vault := GetRandomValidatorNode(NodeActive)
	vault.Type = NodeTypeVault
	c.Assert(k.SetNodeAccount(ctx, vault), IsNil)

	listCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	all, err := k.ListActiveValidators(listCtx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 3)
	countCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	n, err := k.CountActiveValidators(countCtx)
	c.Assert(err, IsNil)
	c.Assert(n, Equals, 3)
	c.Check(countCtx.GasMeter().GasConsumed(), Equals, listCtx.GasMeter().GasConsumed())
}

func (s *KeeperNodeAccountSuite) TestListActiveValidatorsUpTo(c *C) {
	ctx, k := setupKeeperForTest(c)
	for _, status := range []NodeStatus{NodeStandby, NodeActive, NodeActive, types.NodeStatus_Ready, NodeActive, NodeActive} {
		c.Assert(k.SetNodeAccount(ctx, GetRandomValidatorNode(status)), IsNil)
	}
	vault := GetRandomValidatorNode(NodeActive)
	vault.Type = NodeTypeVault
	c.Assert(k.SetNodeAccount(ctx, vault), IsNil)

	gasCtx := func() cosmos.Context { return ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()) }
	allCtx := gasCtx()
	all, err := k.ListActiveValidators(allCtx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 4)
	for _, n := range []int{0, 1, 3, 4, 10} {
		upToCtx := gasCtx()
		got, err := k.ListActiveValidatorsUpTo(upToCtx, n)
		c.Assert(err, IsNil)
		c.Assert(got, DeepEquals, all[:min(n, len(all))], Commentf("n=%d", n))
		// same store access, so the same gas
		c.Check(upToCtx.GasMeter().GasConsumed(), Equals, allCtx.GasMeter().GasConsumed(), Commentf("n=%d", n))
	}
}

func (s *KeeperNodeAccountSuite) TestDetachNodeAccounts(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeStandby),
		GetRandomValidatorNode(NodeActive),
	}
	nas[0].SignerMembership = []string{"a", "b", "c"}
	nas[1].SignerMembership = []string{"d"}
	nas[1].Bond = cosmos.NewUintFromString("340282366920938463463374607431768211457") // 2^128+1: three words
	nas[2].SignerMembership = []string{}
	nas[2].NodeAddress = cosmos.AccAddress{}
	nas[2].Bond = cosmos.ZeroUint()
	nas[3].SignerMembership = nil
	nas[3].NodeAddress = nil
	nas[3].Bond = cosmos.Uint{}
	want := make(NodeAccounts, len(nas))
	for i := range nas {
		want[i] = keeperpkg.CloneNodeAccount(nas[i])
	}

	got := make(NodeAccounts, len(nas))
	copy(got, nas)
	keeperpkg.DetachNodeAccounts(got)
	c.Assert(got, DeepEquals, want)
	for i := range got {
		c.Check(got[i].NodeAddress == nil, Equals, nas[i].NodeAddress == nil, Commentf("%d", i))
		c.Check(got[i].SignerMembership == nil, Equals, nas[i].SignerMembership == nil, Commentf("%d", i))
	}

	// the copies share nothing with their source
	for i := range nas {
		if len(nas[i].NodeAddress) > 0 {
			nas[i].NodeAddress[0] ^= 0xff
		}
		if len(nas[i].SignerMembership) > 0 {
			nas[i].SignerMembership[0] = "changed"
		}
		if !nas[i].Bond.IsNil() {
			nas[i].Bond.BigIntMut().SetInt64(7)
		}
	}
	c.Assert(got, DeepEquals, want)

	// nor with each other: appending to one does not run into the next
	got[0].SignerMembership = append(got[0].SignerMembership, "x")
	got[0].NodeAddress = append(got[0].NodeAddress, 0x01)
	c.Check(got[1].SignerMembership, DeepEquals, want[1].SignerMembership)
	c.Check(got[1].NodeAddress, DeepEquals, want[1].NodeAddress)
	got[0].SignerMembership[1] = "y"
	c.Check(want[0].SignerMembership[1], Equals, "b")
	// growing a Bond in place does not run into the next one's words
	bond := got[0].Bond.BigIntMut()
	bond.Lsh(bond, 128)
	c.Check(got[1].Bond.String(), Equals, want[1].Bond.String())
	bond = got[1].Bond.BigIntMut()
	bond.Lsh(bond, 64)
	c.Check(got[3].Bond.IsNil(), Equals, true)
	c.Check(got[0].Bond.String(), Equals, new(big.Int).Lsh(want[0].Bond.BigInt(), 128).String())
}

func (s *KeeperNodeAccountSuite) TestListValidatorsByStatusCopiesAreDisjoint(c *C) {
	ctx, k := setupKeeperForTest(c)
	for i := 0; i < 3; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.SignerMembership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	}
	want, err := listValidatorsByStatusOracle(ctx, k, NodeActive, -1)
	c.Assert(err, IsNil)
	c.Assert(want, HasLen, 3)
	for pass := 0; pass < 2; pass++ { // memo miss, then hit
		for _, list := range []func() (NodeAccounts, error){
			func() (NodeAccounts, error) { return k.ListActiveValidators(ctx) },
			func() (NodeAccounts, error) { return k.ListActiveValidators(keeperpkg.WithActiveValidatorsCache(ctx)) },
		} {
			got, err := list()
			c.Assert(err, IsNil)
			c.Assert(got, DeepEquals, want, Commentf("pass=%d", pass))
			got[0].SignerMembership = append(got[0].SignerMembership, "x")
			got[0].NodeAddress = append(got[0].NodeAddress, 0x01)
			got[1].SignerMembership[0] = "changed"
			c.Check(got[1].NodeAddress, DeepEquals, want[1].NodeAddress, Commentf("pass=%d", pass))
			c.Check(got[2].SignerMembership, DeepEquals, want[2].SignerMembership, Commentf("pass=%d", pass))
		}
	}
}

// listValidatorsByStatusOracle is ListValidatorsByStatus as on develop
// (unmarshal every record, fail on any that does not decode), keeping at most
// limit matches (limit < 0: all).
func listValidatorsByStatusOracle(ctx cosmos.Context, k KVStore, status NodeStatus, limit int) (NodeAccounts, error) {
	nodeAccounts := make(NodeAccounts, 0)
	naIterator := k.GetNodeAccountIterator(ctx)
	defer naIterator.Close()
	for ; naIterator.Valid(); naIterator.Next() {
		var na NodeAccount
		if err := k.cdc.Unmarshal(naIterator.Value(), &na); err != nil {
			return nodeAccounts, dbError(ctx, "Unmarshal: node account", err)
		}
		full := limit >= 0 && len(nodeAccounts) >= limit
		if !full && na.Type == NodeTypeValidator && na.Status == status {
			nodeAccounts = append(nodeAccounts, na)
		}
	}
	return nodeAccounts, nil
}

func (s *KeeperNodeAccountSuite) TestListValidatorsByStatusMatchesOracle(c *C) {
	ctx, k := setupKeeperForTest(c)
	statuses := []NodeStatus{types.NodeStatus_Unknown, types.NodeStatus_Whitelisted, NodeStandby, types.NodeStatus_Ready, NodeActive, types.NodeStatus_Disabled}
	var nodes NodeAccounts
	for i := 0; i < 40; i++ {
		na := GetRandomValidatorNode(statuses[i%len(statuses)])
		if i%7 == 0 {
			na.Type = NodeTypeVault
		}
		na.SignerMembership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		nodes = append(nodes, na)
	}

	check := func(step string) {
		for _, status := range statuses {
			for _, limit := range []int{-1, 0, 1, 3, 100} {
				wantCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
				want, wantErr := listValidatorsByStatusOracle(wantCtx, k, status, limit)
				for pass := 0; pass < 2; pass++ { // memo miss, then hit
					gotCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
					got, gotErr := k.listValidatorsByStatus(gotCtx, status, limit)
					comment := Commentf("%s status=%s limit=%d pass=%d", step, status, limit, pass)
					c.Assert(gotErr == nil, Equals, wantErr == nil, comment)
					c.Assert(got, DeepEquals, want, comment)
					c.Assert(gotCtx.GasMeter().GasConsumed(), Equals, wantCtx.GasMeter().GasConsumed(), comment)
					if len(got) > 0 { // results are copies, not the memoized values
						got[0].NodeAddress[0] ^= 0xff
						got[0].SignerMembership[0] = "changed"
						got[0].Bond.BigIntMut().SetInt64(1)
					}
				}
				if limit < 0 { // the count reads the store as the listing does
					cntCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
					n, cntErr := k.countValidatorsByStatus(cntCtx, status)
					comment := Commentf("%s count status=%s", step, status)
					c.Assert(cntErr == nil, Equals, wantErr == nil, comment)
					if wantErr == nil {
						c.Assert(n, Equals, len(want), comment)
					}
					c.Assert(cntCtx.GasMeter().GasConsumed(), Equals, wantCtx.GasMeter().GasConsumed(), comment)
				}
			}
		}
	}
	check("initial")

	// changed records are seen
	for i := 0; i < len(nodes); i += 3 {
		nodes[i].Status = statuses[(i+1)%len(statuses)]
		nodes[i].Bond = nodes[i].Bond.AddUint64(1)
		c.Assert(k.SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	check("changed")

	// a standby record whose wire framing is fine but whose custom bond does
	// not decode fails every listing, not just the standby one
	store := k.kvStore(ctx)
	standby := protowire.AppendTag(nil, nodeAccountStatusField, protowire.VarintType)
	standby = protowire.AppendVarint(standby, uint64(NodeStandby))
	badBond := protowire.AppendTag(append([]byte(nil), standby...), nodeAccountBondField, protowire.BytesType)
	badBond = protowire.AppendString(badBond, "x")
	badBondKey := k.GetKey(prefixNodeAccount, "zz-standby-bad-bond")
	store.Set(badBondKey, badBond)
	check("bad bond")
	for _, status := range statuses {
		_, err := k.ListValidatorsByStatus(ctx, status)
		c.Check(err, NotNil, Commentf("status=%s", status))
	}
	store.Delete(badBondKey)

	// records whose bytes are unusual or do not decode; any that does not
	// decode is an error, whatever its status
	for i, bz := range nodeAccountTestBlobs(c, k) {
		if len(bz) == 0 {
			continue // an empty value cannot be stored
		}
		store.Set(k.GetKey(prefixNodeAccount, fmt.Sprintf("zz-blob-%02d", i)), bz)
	}
	store.Set(k.GetKey(prefixNodeAccount, "zz-a-undecodable-standby"), append(standby, 0x4a, 0x05, 'a'))
	check("blobs")
}
