package keeperv1

import (
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/gogoproto/proto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperTssKeysignFailureSuite struct{}

var _ = Suite(&KeeperTssKeysignFailureSuite{})

var errTssKeysignVoterMarshal = errors.New("forced TSS keysign voter marshal failure")

type marshalErrorBinaryCodec struct {
	codec.BinaryCodec
}

func (marshalErrorBinaryCodec) Marshal(_ proto.Message) ([]byte, error) {
	return nil, errTssKeysignVoterMarshal
}

var _ codec.BinaryCodec = marshalErrorBinaryCodec{}

func setRawTssKeysignFailVoter(c *C, ctx cosmos.Context, k KVStore, voter TssKeysignFailVoter) {
	// Bypass the public setter so tests can reproduce legacy and unsupported
	// state bytes that runtime callers are not permitted to write.
	c.Assert(k.setTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, voter.ID), voter), IsNil)
}

func getRawTssKeysignFailVoter(c *C, ctx cosmos.Context, k KVStore, id string) TssKeysignFailVoter {
	var stored TssKeysignFailVoter
	found, err := k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &stored)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	return stored
}

func (KeeperTssKeysignFailureSuite) TestTssKeysignFailVoter(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()

	missing, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Check(missing.State, Equals, types.TssKeysignFailVoterState_PendingVoter)

	live := NewTssKeysignFailVoter(id)
	live.Signers = []string{GetRandomBech32Addr().String()}
	c.Assert(k.SetTssKeysignFailVoter(ctx, live), IsNil)

	stored := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.State, Equals, types.TssKeysignFailVoterState_PendingVoter)
	voter, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, IsNil)
	c.Check(voter.State, Equals, types.TssKeysignFailVoterState_PendingVoter)
	c.Check(voter.Signers, DeepEquals, live.Signers)
}

func (KeeperTssKeysignFailureSuite) TestAllLegacyVoterShapesRemainTerminalAndReadOnly(c *C) {
	ctx, k := setupKeeperForTest(c)
	testCases := []struct {
		name  string
		voter TssKeysignFailVoter
	}{
		{name: "empty terminal", voter: TssKeysignFailVoter{}},
		{name: "positive height terminal", voter: TssKeysignFailVoter{Height: 1024}},
		{name: "in progress shape", voter: TssKeysignFailVoter{Signers: []string{GetRandomBech32Addr().String()}}},
		{name: "terminal round shape", voter: TssKeysignFailVoter{
			Signers:        []string{GetRandomBech32Addr().String(), GetRandomBech32Addr().String()},
			LastRoundCount: 2,
		}},
		{name: "replay polluted terminal shape", voter: TssKeysignFailVoter{
			Signers:        []string{GetRandomBech32Addr().String()},
			LastRoundCount: 1,
		}},
	}

	for i := range testCases {
		tc := testCases[i]
		c.Logf("case: %s", tc.name)
		id := fmt.Sprintf("%064X", i+1)
		tc.voter.ID = id
		setRawTssKeysignFailVoter(c, ctx, k, tc.voter)

		raw := getRawTssKeysignFailVoter(c, ctx, k, id)
		c.Assert(raw.State, Equals, types.TssKeysignFailVoterState_LegacyVoter)

		legacy, err := k.GetTssKeysignFailVoter(ctx, id)
		c.Assert(err, IsNil)
		c.Check(legacy.State, Equals, types.TssKeysignFailVoterState_LegacyVoter)
		c.Check(legacy.Height, Equals, tc.voter.Height)
		c.Check(legacy.Signers, DeepEquals, tc.voter.Signers)
		c.Check(legacy.LastRoundCount, Equals, tc.voter.LastRoundCount)

		// Reads never rewrite legacy records, and the public setter cannot create
		// another ambiguous state-zero record.
		c.Assert(k.SetTssKeysignFailVoter(ctx, legacy), ErrorMatches, "refusing to persist legacy TSS keysign fail voter state")
		c.Check(getRawTssKeysignFailVoter(c, ctx, k, id), DeepEquals, raw)
	}
}

func (KeeperTssKeysignFailureSuite) TestCurrentVoterStatesPersistAcrossKeeperReinstantiation(c *C) {
	ctx, k := setupKeeperForTest(c)
	liveID := fmt.Sprintf("%064X", 1)
	completedID := fmt.Sprintf("%064X", 2)
	live := TssKeysignFailVoter{
		ID:      liveID,
		Signers: []string{GetRandomBech32Addr().String()},
		State:   types.TssKeysignFailVoterState_PendingVoter,
	}
	completed := TssKeysignFailVoter{
		ID:    completedID,
		State: types.TssKeysignFailVoterState_CompletedVoter,
	}
	setRawTssKeysignFailVoter(c, ctx, k, live)
	setRawTssKeysignFailVoter(c, ctx, k, completed)

	// Recreate the keeper over the same store service to prove the encoded state
	// remains readable; this is not a process or database restart test.
	reinstantiated := NewKVStore(k.cdc, k.storeKey, k.coinKeeper, k.accountKeeper, k.upgradeKeeper, k.version)
	for _, reader := range []KVStore{k, reinstantiated} {
		gotLive, err := reader.GetTssKeysignFailVoter(ctx, liveID)
		c.Assert(err, IsNil)
		c.Check(gotLive.State, Equals, types.TssKeysignFailVoterState_PendingVoter)
		c.Check(gotLive.Signers, DeepEquals, live.Signers)

		gotCompleted, err := reader.GetTssKeysignFailVoter(ctx, completedID)
		c.Assert(err, IsNil)
		c.Check(gotCompleted.State, Equals, types.TssKeysignFailVoterState_CompletedVoter)
	}
}

func (KeeperTssKeysignFailureSuite) TestSetterAcceptsOnlyCurrentStates(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	original := TssKeysignFailVoter{
		ID:      id,
		Height:  55,
		Signers: []string{GetRandomBech32Addr().String()},
		State:   types.TssKeysignFailVoterState_PendingVoter,
	}
	c.Assert(k.SetTssKeysignFailVoter(ctx, original), IsNil)
	stored := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.State, Equals, types.TssKeysignFailVoterState_PendingVoter)
	before := stored

	err := k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{ID: id})
	c.Assert(err, ErrorMatches, "refusing to persist legacy TSS keysign fail voter state")
	c.Check(getRawTssKeysignFailVoter(c, ctx, k, id), DeepEquals, before)

	unsupported := types.TssKeysignFailVoterState(3)
	err = k.SetTssKeysignFailVoter(ctx, TssKeysignFailVoter{ID: id, State: unsupported})
	c.Assert(err, ErrorMatches, fmt.Sprintf("refusing to persist unsupported TSS keysign fail voter state: %d", unsupported))
	stored = getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored, DeepEquals, before)
}

func (KeeperTssKeysignFailureSuite) TestSetterReturnsMarshalErrorWithoutRewrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	original := NewTssKeysignFailVoter(id)
	c.Assert(k.SetTssKeysignFailVoter(ctx, original), IsNil)
	before := getRawTssKeysignFailVoter(c, ctx, k, id)

	k.cdc = marshalErrorBinaryCodec{BinaryCodec: k.cdc}
	changed := original
	changed.State = types.TssKeysignFailVoterState_CompletedVoter
	err := k.SetTssKeysignFailVoter(ctx, changed)
	c.Assert(errors.Is(err, errTssKeysignVoterMarshal), Equals, true)

	after := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(after, DeepEquals, before)
}

func (KeeperTssKeysignFailureSuite) TestUnsupportedFutureStateFailsWithoutRewrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	id := GetRandomTxHash().String()
	unsupported := types.TssKeysignFailVoterState(3)
	setRawTssKeysignFailVoter(c, ctx, k, TssKeysignFailVoter{
		ID:    id,
		State: unsupported,
	})

	_, err := k.GetTssKeysignFailVoter(ctx, id)
	c.Assert(err, ErrorMatches, fmt.Sprintf("unsupported TSS keysign fail voter state: %d", unsupported))
	stored := getRawTssKeysignFailVoter(c, ctx, k, id)
	c.Check(stored.State, Equals, unsupported)
}
