package keeperv1

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

type KeySuite struct{}

var _ = Suite(&KeySuite{})

func (s *KeySuite) TestGetKeyMatchesToUpper(c *C) {
	var k KVStore
	// GetKey as it was: prefix + "/" + strings.ToUpper of every part
	want := func(prefix types.DbPrefix, key string, other ...string) []byte {
		out := string(prefix) + "/" + strings.ToUpper(key)
		for _, o := range other {
			out += strings.ToUpper(o)
		}
		return []byte(out)
	}
	keys := []string{
		"", "a", "Z", "abcdefghijklmnopqrstuvwxyz", "@AZ[`az{", "BTC.BTC", "btc.btc", "HaltBTCChain", "TorAnchor-ETH-USDC-0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		"thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", "a/b_c-d~e{|}`@[", "\x00\x7f",
		"straße", "éclair", "ﬀ", "ǅ", "\xff\xfe", "mixed ascii and ü",
	}
	for _, key := range keys {
		for _, other := range [][]string{nil, {"x"}, {"/", "ü"}, {"", "Abc"}} {
			got := k.GetKey(prefixMimir, key, other...)
			c.Check(string(got), Equals, string(want(prefixMimir, key, other...)), Commentf("%q %q", key, other))
		}
	}
}

func (s *KeySuite) TestGetKeyInt64MatchesGetKey(c *C) {
	var k KVStore
	for _, n := range []int64{0, 1, -1, 9, 10, -10, 99, 100, 21_000_000, -21_000_000, math.MaxInt64, math.MinInt64} {
		for _, prefix := range []types.DbPrefix{prefixTxOut, prefixKeygen} {
			got := k.getKeyInt64(prefix, n)
			c.Check(string(got), Equals, string(k.GetKey(prefix, strconv.FormatInt(n, 10))), Commentf("%s %d", prefix, n))
			// exactly sized like GetKey's keys: the store keeps them
			c.Check(cap(got), Equals, len(got), Commentf("%s %d", prefix, n))
		}
	}
}

func (s *KeySuite) TestGetterErrorsNameRecordType(c *C) {
	ctx, k := setupKeeperForTest(c)
	key := []byte("bad/KEY")
	k.kvStore(ctx).Set(key, []byte{0xff, 0xff, 0xff, 0xff})
	// the message the getters built with %T of record
	named := func(err error, record any) {
		c.Assert(err, NotNil)
		want := fmt.Sprintf("KVStore Error: Unmarshal kvstore: (%T) %s: ", record, key)
		c.Check(strings.HasPrefix(err.Error(), want), Equals, true, Commentf("%s", err))
	}

	var i int64
	_, err := k.getInt64(ctx, key, &i)
	named(err, &i)
	var u uint64
	_, err = k.getUint64(ctx, key, &u)
	named(err, &u)
	var addrs []cosmos.AccAddress
	_, err = k.getAccAddresses(ctx, key, &addrs)
	named(err, &addrs)
	var str string
	_, err = k.getString(ctx, string(key), &str)
	named(err, &str)
	var strs []string
	_, err = k.getStrings(ctx, key, &strs)
	named(err, &strs)
	var ui cosmos.Uint
	_, err = k.getUint(ctx, key, &ui)
	named(err, &ui)
}

func (s *KeySuite) TestGettersResetRecord(c *C) {
	ctx, k := setupKeeperForTest(c)
	key := []byte("getters/KEY")
	// a zero value is stored as no bytes: the getters must overwrite the
	// caller's record with it, as decoding through k.cdc resets the wrapper
	for _, n := range []int64{0, -1, math.MinInt64} {
		k.setInt64(ctx, key, n)
		got := int64(7)
		ok, err := k.getInt64(ctx, key, &got)
		c.Assert(err, IsNil)
		c.Check(ok, Equals, true)
		c.Check(got, Equals, n)
	}
	for _, n := range []uint64{0, math.MaxUint64} {
		k.setUint64(ctx, key, n)
		got := uint64(7)
		ok, err := k.getUint64(ctx, key, &got)
		c.Assert(err, IsNil)
		c.Check(ok, Equals, true)
		c.Check(got, Equals, n)
	}
	for _, n := range []cosmos.Uint{cosmos.ZeroUint(), cosmos.NewUint(math.MaxUint64)} {
		k.setUint(ctx, key, n)
		got := cosmos.NewUint(7)
		ok, err := k.getUint(ctx, key, &got)
		c.Assert(err, IsNil)
		c.Check(ok, Equals, true)
		c.Check(got.String(), Equals, n.String())
	}
	addr := GetRandomBech32Addr()
	for _, n := range [][]cosmos.AccAddress{nil, {addr, addr}} {
		k.setAccAddresses(ctx, key, n)
		got := []cosmos.AccAddress{GetRandomBech32Addr()}
		ok, err := k.getAccAddresses(ctx, key, &got)
		c.Assert(err, IsNil)
		c.Check(ok, Equals, true)
		c.Check(got, DeepEquals, n)
	}
	for _, n := range []string{"", "ab"} {
		k.setString(ctx, string(key), n)
		got := "old"
		ok, err := k.getString(ctx, string(key), &got)
		c.Assert(err, IsNil)
		c.Check(ok, Equals, true)
		c.Check(got, Equals, n)
	}
	k.setStrings(ctx, key, []string{"a", ""})
	got := []string{"old"}
	ok, err := k.getStrings(ctx, key, &got)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)
	c.Check(got, DeepEquals, []string{"a", ""})
}

func (s *KeySuite) TestTorAnchorMimirKey(c *C) {
	for _, asset := range []common.Asset{common.BTCAsset, common.ETHAsset, common.TOR, common.RuneAsset()} {
		old := strings.ReplaceAll(fmt.Sprintf("TorAnchor-%s", asset.String()), ".", "-")
		c.Check(TorAnchorMimirKey(asset), Equals, old)
	}
	usdc, err := common.NewAsset("ETH.USDC-0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	c.Assert(err, IsNil)
	c.Check(TorAnchorMimirKey(usdc), Equals, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
}
