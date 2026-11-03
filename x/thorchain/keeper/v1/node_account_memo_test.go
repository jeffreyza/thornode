package keeperv1

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type NodeAccountMemoSuite struct{}

var _ = Suite(&NodeAccountMemoSuite{})

func (s *NodeAccountMemoSuite) TestMatchesCodec(c *C) {
	_, k := setupKeeperForTest(c)
	for i, bz := range nodeAccountTestBlobs(c, k) {
		key := []byte(fmt.Sprintf("memo-test/codec/%d", i))
		var want NodeAccount
		wantErr := k.cdc.Unmarshal(bz, &want)
		for pass := 0; pass < 2; pass++ { // miss, then hit
			got := NodeAccount{Version: "untouched"}
			ok := k.memoDecodeNodeAccount(key, bz, &got)
			c.Assert(ok, Equals, wantErr == nil, Commentf("blob %d pass %d", i, pass))
			if ok {
				c.Assert(got, DeepEquals, want, Commentf("blob %d pass %d", i, pass))
			} else {
				c.Assert(got, DeepEquals, NodeAccount{Version: "untouched"})
			}
		}
	}
}

func (s *NodeAccountMemoSuite) TestMutationIsolation(c *C) {
	_, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.SignerMembership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
	na.Bond = cosmos.NewUint(123456789)
	bz, err := k.cdc.Marshal(&na)
	c.Assert(err, IsNil)
	key := []byte("memo-test/mutation")

	var first NodeAccount
	c.Assert(k.memoDecodeNodeAccount(key, bz, &first), Equals, true)
	c.Assert(first, DeepEquals, na)
	for pass := 0; pass < 2; pass++ {
		var got NodeAccount
		c.Assert(k.memoDecodeNodeAccount(key, bz, &got), Equals, true)
		c.Assert(got, DeepEquals, na)
		got.NodeAddress[0] ^= 0xff
		got.SignerMembership[0] = "changed"
		got.SignerMembership = append(got.SignerMembership, "appended")
		got.Bond.BigIntMut().SetInt64(1)
		got.Status = NodeStandby
	}
	first.NodeAddress[1] ^= 0xff
	first.Bond.BigIntMut().SetInt64(2)

	var got NodeAccount
	c.Assert(k.memoDecodeNodeAccount(key, bz, &got), Equals, true)
	c.Assert(got, DeepEquals, na)
}

func (s *NodeAccountMemoSuite) TestChangedBytesRedecode(c *C) {
	ctx, k := setupKeeperForTest(c)
	na1 := GetRandomValidatorNode(NodeActive)
	na2 := na1
	na2.Bond = na1.Bond.AddUint64(1)
	na2.SignerMembership = []string{GetRandomPubKey().String()}
	bz1, err := k.cdc.Marshal(&na1)
	c.Assert(err, IsNil)
	bz2, err := k.cdc.Marshal(&na2)
	c.Assert(err, IsNil)
	key := []byte("memo-test/changed")

	for i, bz := range [][]byte{bz1, bz2, bz2, bz1, bz2} {
		var want, got NodeAccount
		c.Assert(k.cdc.Unmarshal(bz, &want), IsNil)
		c.Assert(k.memoDecodeNodeAccount(key, bz, &got), Equals, true)
		c.Assert(got, DeepEquals, want, Commentf("step %d", i))
	}

	// a write through the keeper is seen by the next read
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	got, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(got.Bond.Equal(na1.Bond), Equals, true)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	got, err = k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(got.Bond.Equal(na2.Bond), Equals, true)
	c.Assert(got.SignerMembership, DeepEquals, na2.SignerMembership)

	// another store holding other bytes under the same key
	ctx2, k2 := setupKeeperForTest(c)
	c.Assert(k2.SetNodeAccount(ctx2, na1), IsNil)
	for i := 0; i < 3; i++ {
		got, err = k.GetNodeAccount(ctx, na1.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(got.Bond.Equal(na2.Bond), Equals, true)
		got, err = k2.GetNodeAccount(ctx2, na1.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(got.Bond.Equal(na1.Bond), Equals, true)
		c.Assert(got.SignerMembership, DeepEquals, na1.SignerMembership)
	}
}

func (s *NodeAccountMemoSuite) TestBounded(c *C) {
	_, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	bz, err := k.cdc.Marshal(&na)
	c.Assert(err, IsNil)
	for i := 0; i < nodeAccountMemoMaxEntries+10; i++ {
		var got NodeAccount
		c.Assert(k.memoDecodeNodeAccount([]byte(fmt.Sprintf("memo-test/bound/%d", i)), bz, &got), Equals, true)
		c.Assert(nodeAccountDecodeMemo.len() <= nodeAccountMemoMaxEntries, Equals, true)
	}
}

// TestNodeAccountMemoConcurrent is meant for -race: readers decode alternating
// bytes under shared keys and mutate what they get back.
func TestNodeAccountMemoConcurrent(t *testing.T) {
	k := KVStore{cdc: codec.NewProtoCodec(codectypes.NewInterfaceRegistry())}
	var blobs [][]byte
	var wants []NodeAccount
	for i := 0; i < 4; i++ {
		na := types.GetRandomValidatorNode(NodeActive)
		na.SignerMembership = []string{types.GetRandomPubKey().String(), types.GetRandomPubKey().String()}
		bz, err := k.cdc.Marshal(&na)
		if err != nil {
			t.Fatal(err)
		}
		var want NodeAccount
		if err := k.cdc.Unmarshal(bz, &want); err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, bz)
		wants = append(wants, want)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				j := (g + i) % len(blobs)
				key := []byte(fmt.Sprintf("memo-test/concurrent/%d", i%3))
				var got NodeAccount
				if !k.memoDecodeNodeAccount(key, bytes.Clone(blobs[j]), &got) {
					t.Error("decode failed")
					return
				}
				if !reflect.DeepEqual(got, wants[j]) {
					t.Errorf("goroutine %d step %d: mismatch", g, i)
					return
				}
				got.NodeAddress[0]++
				got.SignerMembership[0] = "x"
				got.Bond.BigIntMut().SetInt64(int64(i))
			}
		}(g)
	}
	wg.Wait()
}
