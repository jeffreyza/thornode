package core

import (
	"errors"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type dualLPTestL1Client struct {
	testSwapLiteChainClient
	err   error
	calls int
}

func (c *dualLPTestL1Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.testSwapLiteChainClient.GetAccount(pk)
}

func newDualLPTestCandidate(t *testing.T, name string, rune, doge uint64, l1Err error) (dualLPCandidate, *dualLPTestL1Client) {
	t.Helper()
	user := newTestSwapUser(t, name, common.BTCAsset, sdkmath.ZeroUint(), common.BTCAsset, sdkmath.ZeroUint())
	client := &dualLPTestL1Client{
		testSwapLiteChainClient: testSwapLiteChainClient{
			account: common.NewAccount(0, 0, common.Coins{common.NewCoin(common.DOGEAsset, sdkmath.NewUint(doge))}, true),
		},
		err: l1Err,
	}
	user.ChainClients[common.DOGEChain] = client
	return dualLPCandidate{user: user, runeBalance: sdkmath.NewUint(rune)}, client
}

func assertReleased(t *testing.T, users ...*simtypes.User) {
	t.Helper()
	for _, user := range users {
		if user.IsLocked() {
			t.Fatalf("%s was not released", user.Name())
		}
	}
}

// TestSelectFundedCandidateQueriesL1InRuneOrder keeps the previous choice (the
// funded user with the most RUNE) while querying the shared L1 daemon only
// until that user is found.
func TestSelectFundedCandidateQueriesL1InRuneOrder(t *testing.T) {
	low, lowClient := newDualLPTestCandidate(t, "low", 100, 5, nil)
	high, highClient := newDualLPTestCandidate(t, "high", 300, 0, nil)
	mid, midClient := newDualLPTestCandidate(t, "mid", 200, 8, nil)

	var skips dualLPSkips
	selected := selectFundedCandidate(common.DOGEAsset, []dualLPCandidate{low, high, mid}, &skips)
	if selected == nil || selected.user != mid.user || !selected.l1Balance.Equal(sdkmath.NewUint(8)) {
		t.Fatalf("expected mid with balance 8, got %+v", selected)
	}
	if !mid.user.IsLocked() {
		t.Fatal("selected user must stay acquired")
	}
	assertReleased(t, low.user, high.user)
	if highClient.calls != 1 || midClient.calls != 1 || lowClient.calls != 0 {
		t.Fatalf("unexpected L1 queries: high=%d mid=%d low=%d", highClient.calls, midClient.calls, lowClient.calls)
	}
	if skips.noL1Balance != 1 || skips.lastErr != nil {
		t.Fatalf("unexpected skips: %+v", skips)
	}
}

// TestSelectFundedCandidateStopsOnL1Errors reproduces job 16347608704, where
// each DOGE balance query timed out: repeated failures must end the attempt
// with an error naming the user and chain instead of timing out on every user.
func TestSelectFundedCandidateStopsOnL1Errors(t *testing.T) {
	timeout := errors.New(`fail to get UTXOs: Post "http://127.0.0.1:18332": context deadline exceeded`)
	first, firstClient := newDualLPTestCandidate(t, "first", 300, 5, timeout)
	second, secondClient := newDualLPTestCandidate(t, "second", 200, 5, timeout)
	third, thirdClient := newDualLPTestCandidate(t, "third", 100, 5, timeout)

	var skips dualLPSkips
	selected := selectFundedCandidate(common.DOGEAsset, []dualLPCandidate{first, second, third}, &skips)
	if selected != nil {
		t.Fatalf("expected no selection, got %s", selected.user.Name())
	}
	assertReleased(t, first.user, second.user, third.user)
	if firstClient.calls != 1 || secondClient.calls != 1 || thirdClient.calls != 0 {
		t.Fatalf("unexpected L1 queries: first=%d second=%d third=%d", firstClient.calls, secondClient.calls, thirdClient.calls)
	}
	if skips.l1Errors != dualLPMaxL1Errors {
		t.Fatalf("unexpected skips: %+v", skips)
	}
	if !errors.Is(skips.lastErr, timeout) || !strings.Contains(skips.lastErr.Error(), "second DOGE balance") {
		t.Fatalf("error should name user and chain: %v", skips.lastErr)
	}
}

// TestSelectFundedCandidateSkipsFailingUser keeps a user whose own L1 backend
// fails (as with a per-user XMR wallet) from hiding funded users after it.
func TestSelectFundedCandidateSkipsFailingUser(t *testing.T) {
	broken, _ := newDualLPTestCandidate(t, "broken", 300, 5, errors.New("wallet scan failed"))
	funded, _ := newDualLPTestCandidate(t, "funded", 200, 7, nil)

	var skips dualLPSkips
	selected := selectFundedCandidate(common.DOGEAsset, []dualLPCandidate{broken, funded}, &skips)
	if selected == nil || selected.user != funded.user || !selected.l1Balance.Equal(sdkmath.NewUint(7)) {
		t.Fatalf("expected funded with balance 7, got %+v", selected)
	}
	assertReleased(t, broken.user)
	if skips.l1Errors != 1 {
		t.Fatalf("unexpected skips: %+v", skips)
	}
}

// TestSelectFundedCandidateSkipsUserTakenMeanwhile covers another actor
// acquiring a candidate between the RUNE checks and the L1 query.
func TestSelectFundedCandidateSkipsUserTakenMeanwhile(t *testing.T) {
	taken, takenClient := newDualLPTestCandidate(t, "taken", 300, 5, nil)
	free, _ := newDualLPTestCandidate(t, "free", 200, 5, nil)
	if !taken.user.Acquire() {
		t.Fatal("failed to acquire taken")
	}

	var skips dualLPSkips
	selected := selectFundedCandidate(common.DOGEAsset, []dualLPCandidate{taken, free}, &skips)
	if selected == nil || selected.user != free.user {
		t.Fatalf("expected free, got %+v", selected)
	}
	if !taken.user.IsLocked() || takenClient.calls != 0 || skips.busy != 1 {
		t.Fatalf("taken user must be left to its owner: calls=%d skips=%+v", takenClient.calls, skips)
	}
}

// TestSelectFundedCandidateReportsUnfundedUsers covers job 16353814409, where
// the DOGE chain lost user funding and every user had no L1 balance.
func TestSelectFundedCandidateReportsUnfundedUsers(t *testing.T) {
	first, _ := newDualLPTestCandidate(t, "first", 300, 0, nil)
	second, _ := newDualLPTestCandidate(t, "second", 200, 0, nil)

	var skips dualLPSkips
	selected := selectFundedCandidate(common.DOGEAsset, []dualLPCandidate{first, second}, &skips)
	if selected != nil {
		t.Fatalf("expected no selection, got %s", selected.user.Name())
	}
	assertReleased(t, first.user, second.user)
	if skips.noL1Balance != 2 || skips.lastErr != nil {
		t.Fatalf("unexpected skips: %+v", skips)
	}
}

// TestDualLPWaitStateNamesPendingSide shows the state that made verifyLP wait
// until timeout in job 16347608704: a duplicate DOGE deposit left the position
// with pending_asset set, so it never counted as complete.
func TestDualLPWaitStateNamesPendingSide(t *testing.T) {
	thorAddr := common.Address("tthor1p3pm60jxtqvr6zvlw97l2ef5rm6cn8nfrtt2ds")
	dogeAddr := common.Address("mgdoYxei8iB6PAyGZHoZ57VUrWFWS7bHpR")
	runeAddr, assetAddr := thorAddr.String(), dogeAddr.String()

	if got := dualLPWaitState(nil, thorAddr, dogeAddr); got != "waiting for LP position to be created" {
		t.Fatalf("unexpected state without position: %s", got)
	}

	lps := []openapi.LiquidityProvider{{
		RuneAddress:  &runeAddr,
		AssetAddress: &assetAddr,
		PendingRune:  "0",
		PendingAsset: "2500000000000",
		Units:        "25000000000",
	}}
	if findCompletedLP(lps, thorAddr, dogeAddr) != nil {
		t.Fatal("position with pending asset must not count as complete")
	}
	got := dualLPWaitState(lps, thorAddr, dogeAddr)
	if !strings.Contains(got, "pending_asset=2500000000000") || !strings.Contains(got, "units=25000000000") {
		t.Fatalf("unexpected state for pending position: %s", got)
	}
}
