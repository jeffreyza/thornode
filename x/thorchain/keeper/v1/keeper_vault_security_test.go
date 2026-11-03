package keeperv1

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	storetypes "cosmossdk.io/store/types"
	"google.golang.org/protobuf/encoding/protowire"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperVaultSecuritySuite struct{}

var _ = Suite(&KeeperVaultSecuritySuite{})

// NodeAccount protobuf field numbers of the status and bond.
const (
	nodeAccountStatusField protowire.Number = 2
	nodeAccountBondField   protowire.Number = 5
)

// fullDecodeStatusBond is what GetNodeAccount decodes from the given stored bytes.
func fullDecodeStatusBond(k KVStore, bz []byte) (NodeStatus, cosmos.Uint, error) {
	na := NewNodeAccount(GetRandomBech32Addr(), NodeUnknown, common.PubKeySet{}, "", cosmos.ZeroUint(), "", 18)
	err := k.Cdc().Unmarshal(bz, &na)
	return na.Status, na.Bond, err
}

func appendStatusField(bz []byte, v uint64) []byte {
	bz = protowire.AppendTag(append([]byte{}, bz...), nodeAccountStatusField, protowire.VarintType)
	return protowire.AppendVarint(bz, v)
}

func appendBondField(bz []byte, v string) []byte {
	bz = protowire.AppendTag(append([]byte{}, bz...), nodeAccountBondField, protowire.BytesType)
	return protowire.AppendString(bz, v)
}

// appendRaw appends field num with the given wire type; a bytes field gets
// the given bytes as its value, a varint field the first of them.
func appendRaw(bz []byte, num protowire.Number, typ protowire.Type, v ...byte) []byte {
	bz = protowire.AppendTag(append([]byte{}, bz...), num, typ)
	if typ == protowire.VarintType {
		return protowire.AppendVarint(bz, uint64(v[0]))
	}
	return protowire.AppendBytes(bz, v)
}

func (s *KeeperVaultSecuritySuite) TestGetNodeStatusBondByPubKeyDecodes(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)

	var cases [][]byte
	maxBond := cosmos.NewUintFromBigInt(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)))
	for _, status := range []NodeStatus{NodeUnknown, types.NodeStatus_Whitelisted, NodeStandby, types.NodeStatus_Ready, NodeActive, types.NodeStatus_Disabled} {
		for _, bond := range []cosmos.Uint{cosmos.ZeroUint(), cosmos.NewUint(1), cosmos.NewUint(123456789 * common.One), maxBond} {
			na := GetRandomValidatorNode(status)
			na.Bond = bond
			na.SignerMembership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
			bz, err := k.Cdc().Marshal(&na)
			c.Assert(err, IsNil)
			cases = append(cases, bz)
		}
	}
	base := cases[len(cases)-1]
	unknown := protowire.AppendString(protowire.AppendTag(append([]byte{}, base...), 999, protowire.BytesType), "x")
	cases = append(cases,
		nil, // no fields: unknown status, zero bond
		appendStatusField(nil, uint64(NodeActive)),        // no bond
		appendBondField(nil, "77"),                        // no status
		appendStatusField(base, uint64(NodeStandby)),      // repeated status: last wins
		appendBondField(base, "42"),                       // repeated bond: last wins
		appendBondField(base, ""),                         // empty bond keeps the previous value
		appendStatusField(base, 1<<40|uint64(NodeActive)), // status beyond int32 truncates
		unknown, // unknown field is skipped
		appendBondField(appendStatusField(nil, 4), "0"),     // explicit zero bond
		appendStatusField(appendBondField(nil, "5"), 3),     // bond before status
		appendBondField(base, "abc"),                        // malformed bond
		appendBondField(base, "-1"),                         // negative bond
		appendBondField(base, "1"+strings.Repeat("0", 100)), // bond beyond 256 bits
		base[:len(base)-1],                                  // truncated
		[]byte{0x10},                                        // status tag without a value
		protowire.AppendBytes(protowire.AppendTag(nil, nodeAccountStatusField, protowire.BytesType), []byte{1}), // wrong wire type
		protowire.AppendVarint(protowire.AppendTag(nil, nodeAccountBondField, protowire.VarintType), 1),         // wrong wire type
		appendRaw(base, 1, protowire.VarintType, 7),                                                             // node_address as varint
		appendRaw(base, 9, protowire.VarintType, 7),                                                             // signer_membership as varint
		appendRaw(base, 17, protowire.BytesType, 7),                                                             // maintenance as bytes
		appendRaw(base, 3, protowire.BytesType, 0x08, 0x01),                                                     // pub_key_set.secp256k1 as varint
		appendRaw(base, 3, protowire.BytesType, 0x0a, 0x05),                                                     // pub_key_set.secp256k1 truncated
		appendRaw(base, 3, protowire.BytesType, 0x1a, 0x01, 0x78),                                               // pub_key_set unknown field
		appendRaw(base, 3, protowire.BytesType),                                                                 // empty pub_key_set
		appendRaw(base, 18, protowire.VarintType, 7),                                                            // unknown varint field
	)
	decoded := 0
	for i, bz := range cases {
		wantStatus, wantBond, wantErr := fullDecodeStatusBond(k, bz)
		pk := GetRandomPubKey()
		addr, err := pk.GetThorAddress()
		c.Assert(err, IsNil)
		key := k.GetKey(prefixNodeAccount, addr.String())
		if bz == nil {
			bz = []byte{} // stored, with no fields
		}
		store.Set(key, bz)
		// first read decodes the bytes, the second finds them memoized
		for read := 0; read < 2; read++ {
			status, bond, err := k.getNodeStatusBondByPubKey(ctx, k.kvStore(ctx), pk)
			c.Assert(err != nil, Equals, wantErr != nil, Commentf("case %d read %d", i, read))
			if wantErr != nil {
				_, err1 := k.GetNodeAccountByPubKey(ctx, pk)
				c.Check(err.Error(), Equals, err1.Error(), Commentf("case %d read %d", i, read))
				continue
			}
			c.Check(status, Equals, wantStatus, Commentf("case %d read %d", i, read))
			c.Check(bond.String(), Equals, wantBond.String(), Commentf("case %d read %d", i, read))
		}
		if wantErr == nil {
			decoded++
		}
	}
	c.Check(decoded, Equals, 24+13)
}

func (s *KeeperVaultSecuritySuite) TestGetNodeStatusBondByPubKey(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)

	var pks []common.PubKey
	for _, status := range []NodeStatus{NodeActive, NodeStandby, types.NodeStatus_Disabled} {
		for _, bond := range []cosmos.Uint{cosmos.ZeroUint(), cosmos.NewUint(1234 * common.One)} {
			na := GetRandomValidatorNode(status)
			na.Bond = bond
			c.Assert(k.SetNodeAccount(ctx, na), IsNil)
			pks = append(pks, na.PubKeySet.Secp256k1)
		}
	}
	// missing node account
	pks = append(pks, GetRandomPubKey())
	// stored bytes that do not decode
	corrupt := GetRandomPubKey()
	corruptAddr, err := corrupt.GetThorAddress()
	c.Assert(err, IsNil)
	store.Set(k.GetKey(prefixNodeAccount, corruptAddr.String()), appendBondField(appendStatusField(nil, 4), "abc"))
	pks = append(pks, corrupt)
	// not a pubkey
	pks = append(pks, common.PubKey("bogus"))

	for i, pk := range pks {
		ctx1 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		na, err1 := k.GetNodeAccountByPubKey(ctx1, pk)
		ctx2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		status, bond, err2 := k.getNodeStatusBondByPubKey(ctx2, k.kvStore(ctx2), pk)
		c.Check(ctx2.GasMeter().GasConsumed(), Equals, ctx1.GasMeter().GasConsumed(), Commentf("pk %d", i))
		c.Check(err2 == nil, Equals, err1 == nil, Commentf("pk %d", i))
		if err1 != nil {
			c.Check(err2.Error(), Equals, err1.Error(), Commentf("pk %d", i))
			continue
		}
		c.Check(status, Equals, na.Status, Commentf("pk %d", i))
		c.Check(bond.String(), Equals, na.Bond.String(), Commentf("pk %d", i))
	}
}

func (s *KeeperVaultSecuritySuite) TestGetNodeStatusBondByPubKeyUsesMemo(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	pk := na.PubKeySet.Secp256k1
	addr, err := pk.GetThorAddress()
	c.Assert(err, IsNil)
	key := k.GetKey(prefixNodeAccount, addr.String())

	// a memoized decoding of the stored bytes that differs from what they
	// decode to shows which one is read
	memo := nodeAccountDecodeMemo.shared(k, key, k.kvStore(ctx).Get(key))
	c.Assert(memo, NotNil)
	saved := *memo
	defer func() { *memo = saved }()
	memo.Status = NodeStandby
	memo.Bond = cosmos.NewUint(7)
	status, bond, err := k.getNodeStatusBondByPubKey(ctx, k.kvStore(ctx), pk)
	c.Assert(err, IsNil)
	c.Check(status, Equals, NodeStandby)
	c.Check(bond.String(), Equals, "7")

	// the bond handed out is a copy
	bond.BigIntMut().SetUint64(99)
	c.Check(memo.Bond.String(), Equals, "7")
	_, bond, err = k.getNodeStatusBondByPubKey(ctx, k.kvStore(ctx), pk)
	c.Assert(err, IsNil)
	c.Check(bond.String(), Equals, "7")

	// changed bytes are decoded again
	na.Bond = cosmos.NewUint(2000 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	status, bond, err = k.getNodeStatusBondByPubKey(ctx, k.kvStore(ctx), pk)
	c.Assert(err, IsNil)
	c.Check(status, Equals, NodeActive)
	c.Check(bond.String(), Equals, na.Bond.String())
}

func (s *KeeperVaultSecuritySuite) TestGetPoolMemo(c *C) {
	ctx, k := setupKeeperForTest(c)
	store := k.kvStore(ctx)

	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	for i, asset := range []common.Asset{common.ETHAsset, common.BTCAsset, usdc} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(uint64(i+1) * 100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(uint64(i+1) * 7 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}
	// stored bytes that do not decode
	store.Set(k.GetKey(prefixPool, common.DOGEAsset.String()), []byte{0x0a})

	memo := make(map[common.Asset]*poolMemo)
	check := func(asset common.Asset) {
		ctx1 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		want, err1 := k.GetPool(ctx1, asset)
		ctx2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		got, err2 := k.getPoolMemo(ctx2, k.kvStore(ctx2), asset, memo)
		c.Check(ctx2.GasMeter().GasConsumed(), Equals, ctx1.GasMeter().GasConsumed(), Commentf("%s", asset))
		c.Check(err2 == nil, Equals, err1 == nil, Commentf("%s", asset))
		if err1 != nil {
			c.Check(err2.Error(), Equals, err1.Error(), Commentf("%s", asset))
		}
		c.Check(got, DeepEquals, want, Commentf("%s", asset))
	}
	// the last three: synth and missing, then undecodable
	assets := []common.Asset{common.ETHAsset, common.BTCAsset, usdc, common.ETHAsset.GetSyntheticAsset(), common.LTCAsset, common.DOGEAsset}
	for i := 0; i < 2; i++ { // decoded, then from memo
		for _, asset := range assets {
			check(asset)
		}
	}
	// each asset's key is built once, the pools that decode decoded once
	c.Assert(memo, HasLen, len(assets))
	decoded := 0
	for _, asset := range assets {
		m := memo[asset]
		c.Check(string(m.key), Equals, string(k.GetKey(prefixPool, asset.String())), Commentf("%s", asset))
		if m.decoded {
			decoded++
		}
	}
	c.Check(decoded, Equals, 3)
	ethKey := &memo[common.ETHAsset].key[0]

	// a changed pool is decoded again
	pool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(3)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	check(common.ETHAsset)
	got, err := k.getPoolMemo(ctx, k.kvStore(ctx), common.ETHAsset, memo)
	c.Assert(err, IsNil)
	c.Check(got.BalanceAsset.Uint64(), Equals, uint64(3))
	c.Check(&memo[common.ETHAsset].key[0] == ethKey, Equals, true)
}

// getSortedVaultSecurityOracle is getSortedVaultSecurity as on develop.
func getSortedVaultSecurityOracle(k KVStore, ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) []VaultSecurity {
	vaultSecurity := make([]VaultSecurity, len(vaults))

	for i, vault := range vaults {
		// get total bond
		totalBond := cosmos.ZeroUint()
		for _, pk := range vault.GetMembership() {
			na, err := k.GetNodeAccountByPubKey(ctx, pk)
			if err != nil {
				ctx.Logger().Error("failed to get node account by pubkey", "error", err)
				continue
			}
			if na.Status == NodeActive {
				totalBond = totalBond.Add(na.Bond)
			}
		}

		// get total value
		totalValue := cosmos.ZeroUint()
		for _, coin := range vault.Coins {
			if coin.IsRune() {
				continue
			} else {
				pool, err := k.GetPool(ctx, coin.Asset)
				if err != nil {
					ctx.Logger().Error("failed to get pool", "error", err)
					continue
				}
				totalValue = totalValue.Add(pool.AssetValueInRune(coin.Amount))
			}
		}

		// add recent unsent txout items to totalValue
		h := ctx.BlockHeight() - signingTransPeriod
		if h < 1 {
			h = 1
		}
		for height := h; height <= ctx.BlockHeight(); height += 1 {
			txOut, err := k.GetTxOut(ctx, height)
			if err != nil {
				ctx.Logger().Error("unable to get txout", "error", err)
				continue
			}
			for _, item := range txOut.TxArray {
				if item.OutHash.IsEmpty() {
					var toAddress common.Address
					toAddress, err = vault.GetAddress(item.Coin.Asset.GetChain())
					if err != nil {
						ctx.Logger().Error("failed to get address of chain", "error", err)
						continue
					}
					if item.VaultPubKey.Equals(vault.PubKey) {
						if item.Coin.IsRune() {
							totalValue = common.SafeSub(totalValue, item.Coin.Amount)
						} else {
							var pool Pool
							pool, err = k.GetPool(ctx, item.Coin.Asset)
							if err != nil {
								ctx.Logger().Error("failed to get pool", "error", err)
								continue
							}
							totalValue = common.SafeSub(totalValue, pool.AssetValueInRune(item.Coin.Amount))
						}
					} else if item.ToAddress.Equals(toAddress) {
						if item.Coin.IsRune() {
							totalValue = totalValue.Add(item.Coin.Amount)
						} else {
							var pool Pool
							pool, err = k.GetPool(ctx, item.Coin.Asset)
							if err != nil {
								ctx.Logger().Error("failed to get pool", "error", err)
								continue
							}
							totalValue = totalValue.Add(pool.AssetValueInRune(item.Coin.Amount))
						}
					}
				}
			}
		}

		if totalValue.GT(totalBond) {
			diff := totalValue.Sub(totalBond).BigInt()
			vaultSecurity[i] = VaultSecurity{
				Vault:      vault,
				TotalBond:  totalBond,
				TotalValue: totalValue,
				Diff:       cosmos.NewIntFromBigInt(diff).MulRaw(-1),
			}
		} else {
			diff := totalBond.Sub(totalValue).BigInt()
			vaultSecurity[i] = VaultSecurity{
				Vault:      vault,
				TotalBond:  totalBond,
				TotalValue: totalValue,
				Diff:       cosmos.NewIntFromBigInt(diff),
			}
		}
	}

	// sort by how far total bond and total value are from each other
	sort.SliceStable(vaultSecurity, func(i, j int) bool {
		return vaultSecurity[i].Diff.LT(vaultSecurity[j].Diff)
	})

	return vaultSecurity
}

// getSortedVaultSecurity matches develop's result and its store access, so its
// gas: inside a tx gas_used is consensus-visible.
func (s *KeeperVaultSecuritySuite) TestGetSortedVaultSecurityMatchesDevelop(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100)
	store := k.kvStore(ctx)

	for i, asset := range []common.Asset{common.ETHAsset, common.BTCAsset} {
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(uint64(i+1) * 100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(uint64(i+1) * 7 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}

	var vaults Vaults
	for v := 0; v < 3; v++ {
		vault := types.GetRandomVault()
		vault.Membership = nil
		for m := 0; m < 3; m++ {
			na := GetRandomValidatorNode(NodeActive)
			if m == 2 {
				na.Status = NodeStandby
			}
			na.Bond = cosmos.NewUint(uint64(v*10+m+1) * common.One)
			c.Assert(k.SetNodeAccount(ctx, na), IsNil)
			vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
		}
		vault.Coins = common.NewCoins(
			common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(v+1)*common.One)),
			common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(3-v)*common.One)),
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)),
		)
		vaults = append(vaults, vault)
	}

	// pending items paid by a vault, paid to a vault, and already sent ones,
	// across the signing window; one height whose bytes do not decode
	for height := int64(80); height <= 100; height += 4 {
		txOut := NewTxOut(height)
		for v, vault := range vaults {
			ethAddr, err := vault.PubKey.GetAddress(common.ETHChain)
			c.Assert(err, IsNil)
			txOut.TxArray = append(txOut.TxArray,
				TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, ToAddress: GetRandomETHAddress(), Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(v+1)*common.One/10))},
				TxOutItem{Chain: common.ETHChain, VaultPubKey: vaults[(v+1)%len(vaults)].PubKey, ToAddress: ethAddr, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One/20))},
				TxOutItem{Chain: common.THORChain, VaultPubKey: vault.PubKey, ToAddress: GetRandomTHORAddress(), Coin: common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))},
				TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, ToAddress: GetRandomETHAddress(), Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)), OutHash: GetRandomTxHash()},
			)
		}
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}
	store.Set(k.GetKey(prefixTxOut, "90"), []byte{0x0a})

	for _, cached := range []bool{false, true} {
		for _, period := range []int64{-1, 0, 5, 20, 300} {
			// every store access charges gas: the same charges in the same order
			// are the same store access
			gas1 := newGasLog()
			ctx1 := ctx.WithGasMeter(gas1)
			gas2 := newGasLog()
			ctx2 := ctx.WithGasMeter(gas2)
			if cached {
				ctx1, ctx2 = keeper.WithTxOutCache(ctx1), keeper.WithTxOutCache(ctx2)
			}
			want := getSortedVaultSecurityOracle(k, ctx1, vaults, period)
			got := k.getSortedVaultSecurity(ctx2, vaults, period)
			c.Check(gas2.charges, DeepEquals, gas1.charges, Commentf("cached %v period %d", cached, period))
			c.Assert(got, HasLen, len(want))
			for i := range want {
				c.Check(got[i].Vault.PubKey, Equals, want[i].Vault.PubKey, Commentf("period %d #%d", period, i))
				c.Check(got[i].TotalBond.String(), Equals, want[i].TotalBond.String(), Commentf("period %d #%d", period, i))
				c.Check(got[i].TotalValue.String(), Equals, want[i].TotalValue.String(), Commentf("period %d #%d", period, i))
				c.Check(got[i].Diff.String(), Equals, want[i].Diff.String(), Commentf("period %d #%d", period, i))
			}
		}
	}
}

// gasLog is an infinite gas meter that records every charge, in order.
type gasLog struct {
	storetypes.GasMeter
	charges []string
}

func newGasLog() *gasLog {
	return &gasLog{GasMeter: storetypes.NewInfiniteGasMeter()}
}

func (g *gasLog) ConsumeGas(amount storetypes.Gas, descriptor string) {
	g.charges = append(g.charges, fmt.Sprintf("%s %d", descriptor, amount))
	g.GasMeter.ConsumeGas(amount, descriptor)
}

// getTxOutBytesOracle is getTxOutBytes as on develop.
func getTxOutBytesOracle(k KVStore, ctx cosmos.Context, height int64) (key, bz []byte, present bool) {
	key = k.GetKey(prefixTxOut, strconv.FormatInt(height, 10))
	cache := keeper.TxOutCacheFromContext(ctx)
	if cache != nil {
		var ok bool
		if bz, present, ok = cache.Get(height); ok {
			return key, bz, present
		}
	}
	store := k.kvStore(ctx)
	present = store.Has(key)
	if present {
		bz = store.Get(key)
	}
	if cache != nil {
		cache.Set(height, bz, present)
	}
	return key, bz, present
}

// getTxOutBytes and a txOutWindow read return what develop's getTxOutBytes
// did, with the same store access, with or without a TxOutCache.
func (s *KeeperVaultSecuritySuite) TestTxOutWindowMatchesDevelop(c *C) {
	ctx, k := setupKeeperForTest(c)
	for _, height := range []int64{8, 10, 11} {
		txOut := NewTxOut(height)
		txOut.TxArray = append(txOut.TxArray, TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(height)*common.One)),
		})
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}
	// stored bytes that do not decode
	k.kvStore(ctx).Set(k.getKeyInt64(prefixTxOut, 12), []byte{0x0a})

	for _, cached := range []bool{false, true} {
		var ctxs [3]cosmos.Context
		var gas [3]*gasLog
		for i := range ctxs {
			gas[i] = newGasLog()
			ctxs[i] = ctx.WithGasMeter(gas[i])
			if cached {
				ctxs[i] = keeper.WithTxOutCache(ctxs[i])
			}
		}
		window := k.newTxOutWindow(ctxs[2], 7, 13)
		c.Assert(window.keys, HasLen, 7)
		for pass := 0; pass < 2; pass++ { // with a cache, the second pass is served by it
			for height := int64(13); height >= 7; height-- {
				key, bz, present := getTxOutBytesOracle(k, ctxs[0], height)
				c.Check(present, Equals, height >= 8 && height != 9 && height != 13, Commentf("height %d", height))
				for i, read := range []func(int64) ([]byte, []byte, bool){
					func(h int64) ([]byte, []byte, bool) { return k.getTxOutBytes(ctxs[1], h) },
					window.get,
				} {
					comment := Commentf("cached %v pass %d height %d reader %d", cached, pass, height, i)
					key2, bz2, present2 := read(height)
					c.Check(string(key2), Equals, string(key), comment)
					c.Check(bz2, DeepEquals, bz, comment)
					c.Check(present2, Equals, present, comment)
				}
			}
		}
		c.Check(gas[0].charges, Not(HasLen), 0)
		c.Check(gas[1].charges, DeepEquals, gas[0].charges, Commentf("cached %v", cached))
		c.Check(gas[2].charges, DeepEquals, gas[0].charges, Commentf("cached %v", cached))
	}

	// keys of any width, each clipped to its length; and an empty window
	for _, r := range [][2]int64{{-12, 3}, {-3, 12}, {98, 1001}, {math.MaxInt64 - 2, math.MaxInt64}, {math.MinInt64, math.MinInt64 + 2}} {
		window := k.newTxOutWindow(ctx, r[0], r[1])
		c.Assert(window.keys, HasLen, int(r[1]-r[0]+1))
		for i, key := range window.keys {
			c.Check(string(key), Equals, string(k.GetKey(prefixTxOut, strconv.FormatInt(r[0]+int64(i), 10))), Commentf("%v #%d", r, i))
			c.Check(cap(key), Equals, len(key), Commentf("%v #%d", r, i))
		}
	}
	c.Check(k.newTxOutWindow(ctx, 5, 4).keys, HasLen, 0)
}

// getPendingOutboundsOracle is GetPendingOutbounds as on develop.
func getPendingOutboundsOracle(k KVStore, ctx cosmos.Context, asset common.Asset) []TxOutItem {
	signingPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	standardStartHeight := ctx.BlockHeight() - signingPeriod
	if standardStartHeight < 1 {
		standardStartHeight = 1
	}
	// Use the longest period the requested asset's underlying chain can contain.
	// GetChain reports THOR for synth, trade, and secured assets, so use the L1
	// chain to retain any EVM/XMR-backed liabilities conservatively.
	scanPeriod := types.MaxTxOutSigningTransactionPeriodForChain(asset.GetLayer1Asset().Chain, signingPeriod)
	startHeight := ctx.BlockHeight() - scanPeriod
	if startHeight < 1 {
		startHeight = 1
	}
	txOutDelayMax := k.GetConfigInt64(ctx, constants.TxOutDelayMax)
	maxTxOutOffset := k.GetConfigInt64(ctx, constants.MaxTxOutOffset)
	var outbounds []TxOutItem
	seen := make(map[string]struct{})
	// The rolling window spans the longest timing class, while each item retains
	// only its own signing period. Durable stale migrate markers are added below.
	appendPendingOutbounds := func(blockOut *TxOut) {
		for i, txOutItem := range blockOut.TxArray {
			// only need to look at outbounds for the same asset
			if !txOutItem.Coin.Asset.Equals(asset) {
				continue
			}
			// only still outstanding txout will be considered
			if !txOutItem.OutHash.IsEmpty() {
				continue
			}
			if ctx.BlockHeight()-blockOut.Height > txOutItem.SigningTransactionPeriod(signingPeriod) {
				continue
			}
			itemKey := strconv.FormatInt(blockOut.Height, 10) + "/" + strconv.Itoa(i)
			seen[itemKey] = struct{}{}
			outbounds = append(outbounds, txOutItem)
		}
	}
	for height := startHeight; height <= ctx.BlockHeight()+txOutDelayMax; height++ {
		blockOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get block tx out", "error", err)
		}
		if height > ctx.BlockHeight()+maxTxOutOffset && len(blockOut.TxArray) == 0 {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		appendPendingOutbounds(blockOut)
	}

	if signingPeriod <= 0 {
		return outbounds
	}

	staleMigratePendingBlocks := signingPeriod * constants.StaleMigratePendingSigningPeriodMultiplier
	// Build a unique set of old pending migrate heights before loading txouts.
	// The same height can appear multiple times for multi-asset migration, but
	// processing a txout block more than once would double-deduct availability.
	stalePendingVaultsByHeight := make(map[int64]map[common.PubKey]struct{})
	stalePendingHeights := make([]int64, 0)
	retiring, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		ctx.Logger().Error("fail to get retiring vaults for pending outbounds", "error", err)
		return outbounds
	}
	for _, vault := range retiring {
		for _, height := range vault.PendingTxBlockHeights {
			if height < 1 || height >= standardStartHeight {
				continue
			}
			// Pending migrate heights are durable references beyond the rolling
			// signing window, but expired ones are retry-eligible in migrateFunds
			// and must not reserve funds forever.
			if ctx.BlockHeight()-height > staleMigratePendingBlocks {
				continue
			}
			eligibleVaults, ok := stalePendingVaultsByHeight[height]
			if !ok {
				eligibleVaults = make(map[common.PubKey]struct{})
				stalePendingVaultsByHeight[height] = eligibleVaults
				stalePendingHeights = append(stalePendingHeights, height)
			}
			eligibleVaults[vault.PubKey] = struct{}{}
		}
	}
	// Sort for deterministic accounting. This helper feeds vault-selection logic,
	// so consensus-sensitive callers must not depend on map iteration order.
	sort.Slice(stalePendingHeights, func(i, j int) bool {
		return stalePendingHeights[i] < stalePendingHeights[j]
	})
	for _, height := range stalePendingHeights {
		blockOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get stale pending migrate tx out", "error", err, "height", height)
			continue
		}
		eligibleVaults := stalePendingVaultsByHeight[height]
		for i, txOutItem := range blockOut.TxArray {
			if !txOutItem.Coin.Asset.Equals(asset) || !txOutItem.OutHash.IsEmpty() {
				continue
			}
			if _, ok := eligibleVaults[txOutItem.VaultPubKey]; !ok {
				continue
			}
			// Only count the exact migrate item referenced by the durable marker.
			// Other old txouts at the same height should not reserve this vault's funds.
			if !isMigrateMemoForHeight(txOutItem.Memo, height) {
				continue
			}
			itemKey := strconv.FormatInt(blockOut.Height, 10) + "/" + strconv.Itoa(i)
			if _, ok := seen[itemKey]; ok {
				continue
			}
			seen[itemKey] = struct{}{}
			outbounds = append(outbounds, txOutItem)
		}
	}
	return outbounds
}

// GetPendingOutbounds returns what develop's did, with the same store access,
// with or without a TxOutCache: absent, empty, partly decodable and
// undecodable heights, a walk past the max offset, and stale migrate heights.
func (s *KeeperVaultSecuritySuite) TestGetPendingOutboundsMatchesDevelop(c *C) {
	ctx, k := setupKeeperForTest(c)
	signingPeriod := k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	maxOffset := k.GetConstants().GetInt64Value(constants.MaxTxOutOffset)
	ctx = ctx.WithBlockHeight(signingPeriod*3 + 10)
	store := k.kvStore(ctx)
	vaultPubKey := GetRandomPubKey()
	item := func(asset common.Asset, n uint64, sent bool) TxOutItem {
		toi := TxOutItem{
			Chain:       asset.GetChain(),
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: vaultPubKey,
			Coin:        common.NewCoin(asset, cosmos.NewUint(n*common.One)),
			InHash:      GetRandomTxHash(),
		}
		if sent {
			toi.OutHash = GetRandomTxHash()
		}
		return toi
	}
	setTxOut := func(height int64, items ...TxOutItem) {
		txOut := NewTxOut(height)
		txOut.TxArray = items
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}

	// the signing window, the window of longer-lived classes, and future heights
	bh := ctx.BlockHeight()
	for _, height := range []int64{bh - signingPeriod*2, bh - signingPeriod - 3, bh - signingPeriod + 1, bh - 7, bh, bh + 1, bh + 30, bh + maxOffset} {
		setTxOut(height, item(common.ETHAsset, uint64(height%7)+1, false), item(common.BTCAsset, 2, false), item(common.ETHAsset, 3, true))
	}
	// heights past the max offset are read while they hold items
	setTxOut(bh+maxOffset+1, item(common.ETHAsset, 4, false))
	setTxOut(bh+maxOffset+2, item(common.BTCAsset, 5, false))
	// a TxOut whose second item is cut short decodes its first, with an error
	partial := NewTxOut(bh - 3)
	partial.TxArray = []TxOutItem{item(common.ETHAsset, 6, false), item(common.ETHAsset, 7, false)}
	bz, err := k.Cdc().Marshal(partial)
	c.Assert(err, IsNil)
	store.Set(k.getKeyInt64(prefixTxOut, bh-3), bz[:len(bz)-3])
	// stored bytes that do not decode at all
	store.Set(k.getKeyInt64(prefixTxOut, bh-2), []byte{0x0a})
	// a stored TxOut without items
	empty, err := k.Cdc().Marshal(NewTxOut(bh - 1))
	c.Assert(err, IsNil)
	store.Set(k.getKeyInt64(prefixTxOut, bh-1), empty)

	// a retiring vault's stale migrate heights
	retiring := NewVault(bh-1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), nil)
	staleHeight := bh - signingPeriod - 5
	retiring.PendingTxBlockHeights = []int64{staleHeight, staleHeight}
	c.Assert(k.SetVault(ctx, retiring), IsNil)
	migrate := item(common.ETHAsset, 8, false)
	migrate.VaultPubKey = retiring.PubKey
	migrate.Memo = fmt.Sprintf("MIGRATE:%d", staleHeight)
	setTxOut(staleHeight, migrate, item(common.ETHAsset, 9, false))

	check := func(stop string) {
		for _, cached := range []bool{false, true} {
			gas1 := newGasLog()
			ctx1 := ctx.WithGasMeter(gas1)
			gas2 := newGasLog()
			ctx2 := ctx.WithGasMeter(gas2)
			if cached {
				ctx1, ctx2 = keeper.WithTxOutCache(ctx1), keeper.WithTxOutCache(ctx2)
			}
			for pass := 0; pass < 2; pass++ { // with a cache, the second pass is served by it
				for _, asset := range []common.Asset{common.ETHAsset, common.BTCAsset, common.XRPAsset} {
					comment := Commentf("stop %s cached %v pass %d %s", stop, cached, pass, asset)
					want := getPendingOutboundsOracle(k, ctx1, asset)
					got := k.GetPendingOutbounds(ctx2, asset)
					c.Check(got, DeepEquals, want, comment)
					if asset.Equals(common.ETHAsset) {
						c.Check(want, HasLen, 9, comment) // incl. the partly decoded and the migrate item
					}
				}
			}
			c.Check(gas2.charges, DeepEquals, gas1.charges, Commentf("stop %s cached %v", stop, cached))
		}
	}
	// the walk stops at the first empty height past the max offset
	check("absent")
	store.Set(k.getKeyInt64(prefixTxOut, bh+maxOffset+3), empty)
	setTxOut(bh+maxOffset+4, item(common.ETHAsset, 10, false))
	check("empty")
}

// A txOutWalk read is GetTxOut, except that a height with no TxOut stored
// reads as nil; its keys are getKeyInt64's, clipped, across key buffers.
func (s *KeeperVaultSecuritySuite) TestTxOutWalkMatchesGetTxOut(c *C) {
	ctx, k := setupKeeperForTest(c)
	setTxOut := func(height int64) {
		txOut := NewTxOut(height)
		txOut.TxArray = append(txOut.TxArray, TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: GetRandomPubKey(),
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(uint64(height)*common.One)),
		})
		c.Assert(k.SetTxOut(ctx, txOut), IsNil)
	}
	for height := int64(1); height <= 200; height += 9 {
		setTxOut(height)
	}
	k.kvStore(ctx).Set(k.getKeyInt64(prefixTxOut, 50), []byte{0x0a})

	gas1 := newGasLog()
	ctx1 := ctx.WithGasMeter(gas1)
	gas2 := newGasLog()
	ctx2 := ctx.WithGasMeter(gas2)
	walk := k.newTxOutWalk(ctx2)
	for height := int64(-3); height <= 200; height++ {
		comment := Commentf("height %d", height)
		want, err1 := k.GetTxOut(ctx1, height)
		got, err2 := k.getTxOutWalk(ctx2, walk, height)
		c.Check(err2 == nil, Equals, err1 == nil, comment)
		if err1 != nil {
			c.Check(err2.Error(), Equals, err1.Error(), comment)
		}
		if k.has(ctx, k.getKeyInt64(prefixTxOut, height)) {
			c.Check(got, DeepEquals, want, comment)
		} else {
			c.Check(got, IsNil, comment)
			c.Check(want, DeepEquals, NewTxOut(height), comment)
		}
	}
	c.Check(gas2.charges, DeepEquals, gas1.charges)

	// keys are getKeyInt64's, clipped, and stay intact across key buffers
	heights := []int64{math.MinInt64, math.MaxInt64}
	for height := int64(-3); height <= 200; height++ {
		heights = append(heights, height)
	}
	walk = k.newTxOutWalk(ctx)
	keys := make([][]byte, len(heights))
	for i, height := range heights {
		keys[i] = walk.key(height)
		c.Check(cap(keys[i]), Equals, len(keys[i]), Commentf("height %d", height))
	}
	for i, height := range heights {
		c.Check(string(keys[i]), Equals, string(k.getKeyInt64(prefixTxOut, height)), Commentf("height %d", height))
	}
}
