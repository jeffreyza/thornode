package features

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
)

// initInstantSendFundingTest restores the global config after its environment resets.
func initInstantSendFundingTest(t *testing.T) {
	t.Helper()
	t.Cleanup(config.Init)
	t.Setenv("CHAIN_ID", "thorchain-mocknet-v1")
	config.Init()
}

// newInstantSendFundingUser uses the real Dash client against an account-only RPC.
func newInstantSendFundingUser(t *testing.T, balance *atomic.Uint64, fail *atomic.Bool) *User {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "getaddressinfo":
			response["result"] = map[string]any{"iswatchonly": true}
		case "listunspent":
			if fail.Load() {
				response["error"] = map[string]any{"code": -1, "message": "account unavailable"}
				break
			}
			amount := balance.Load()
			response["result"] = []map[string]any{{
				"amount": json.Number(fmt.Sprintf("%d.%08d", amount/common.One, amount%common.One)),
			}}
		default:
			t.Errorf("account selection made unexpected RPC %s", request.Method)
			response["error"] = map[string]any{"code": -32601, "message": "unexpected method"}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return NewUser(strings.Repeat("dog ", 23)+"fossil", map[common.Chain]LiteChainClientConstructor{
		common.DASHChain: utxo.NewConstructor(server.URL),
	})
}

// TestInstantSendRequiredBalance checks the complete retry budget and fee changes.
func TestInstantSendRequiredBalance(t *testing.T) {
	a, _ := newInstantSendTestActor()
	for _, tc := range []struct {
		fee, want uint64
	}{
		{15_000, 5_800_306_000}, // 58 DASH plus all thirty fees
		{0, 5_800_006_000},
		{30_000, 5_800_606_000},
	} {
		if got := a.requiredBalance(tc.fee); got != tc.want {
			t.Fatalf("fee=%d: budget=%d, want %d", tc.fee, got, tc.want)
		}
	}
}

// TestInstantSendAcquireUserChecksBalance covers balance boundaries, RPC failure
// and busy accounts without changing the production client's concrete type.
func TestInstantSendAcquireUserChecksBalance(t *testing.T) {
	initInstantSendFundingTest(t)
	for _, tc := range []struct {
		name          string
		amount        uint64
		fail, busy    bool
		wantSelection bool
	}{
		{name: "old seed insufficient", amount: 10 * common.One},
		{name: "one duff short", amount: 5_800_305_999},
		{name: "exact budget", amount: 5_800_306_000, wantSelection: true},
		{name: "new seed", amount: 100 * common.One, wantSelection: true},
		{name: "account read fails", amount: 100 * common.One, fail: true},
		{name: "account busy", amount: 100 * common.One, busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var balance atomic.Uint64
			var fail atomic.Bool
			balance.Store(tc.amount)
			fail.Store(tc.fail)
			user := newInstantSendFundingUser(t, &balance, &fail)
			if tc.busy {
				if !user.Acquire() {
					t.Fatal("failed to reserve busy user")
				}
				t.Cleanup(user.Release)
			}
			a, _ := newInstantSendTestActor()
			result := a.acquireUser(&OpConfig{Users: []*User{user}})
			if result.Finish || result.Continue != tc.wantSelection {
				t.Fatalf("unexpected acquisition result: %+v", result)
			}
			if tc.wantSelection {
				if result.Error != nil || a.account != user || a.dash == nil || !user.IsLocked() {
					t.Fatalf("funded account was not reserved: %+v", result)
				}
				a.releaseAccount(nil)
			} else {
				if result.Error == nil || !strings.Contains(result.Error.Error(), "retry budget") {
					t.Fatalf("missing acquisition diagnostic: %+v", result)
				}
				if a.account != nil || a.dash != nil || user.IsLocked() != tc.busy {
					t.Fatal("rejected account was retained or another actor's lock was released")
				}
			}
		})
	}
}

// TestInstantSendAcquireUserTriesNextAccount ensures one unavailable account does
// not prevent using a later funded account, and releases the unsuccessful candidate.
func TestInstantSendAcquireUserTriesNextAccount(t *testing.T) {
	initInstantSendFundingTest(t)
	for _, readFails := range []bool{false, true} {
		var poorBalance, richBalance atomic.Uint64
		var poorFailure, richFailure atomic.Bool
		poorBalance.Store(10 * common.One)
		richBalance.Store(100 * common.One)
		poorFailure.Store(readFails)
		poor := newInstantSendFundingUser(t, &poorBalance, &poorFailure)
		rich := newInstantSendFundingUser(t, &richBalance, &richFailure)
		a, _ := newInstantSendTestActor()
		result := a.acquireUser(&OpConfig{Users: []*User{poor, rich}})
		if !result.Continue || result.Error != nil || a.account != rich || poor.IsLocked() || !rich.IsLocked() {
			t.Fatalf("failed to select the later funded user: %+v", result)
		}
		a.releaseAccount(nil)
	}
}

// TestInstantSendAcquireUserRetriesFunding ensures a later poll can select an
// account once funds become available, without reserving it during the wait.
func TestInstantSendAcquireUserRetriesFunding(t *testing.T) {
	initInstantSendFundingTest(t)
	var balance atomic.Uint64
	var fail atomic.Bool
	user := newInstantSendFundingUser(t, &balance, &fail)
	a, _ := newInstantSendTestActor()
	opConfig := &OpConfig{Users: []*User{user}}
	result := a.acquireUser(opConfig)
	if result.Continue || result.Finish || result.Error == nil || user.IsLocked() {
		t.Fatalf("unfunded account must remain available for retry: %+v", result)
	}
	balance.Store(100 * common.One)
	result = a.acquireUser(opConfig)
	if !result.Continue || result.Error != nil || a.account != user {
		t.Fatalf("funded account was not selected on retry: %+v", result)
	}
	a.releaseAccount(nil)
}
