package core

import (
	"encoding/json"
	"errors"
	"fmt"
	rand "math/rand/v2"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

func TestXMRActiveVaultReady(t *testing.T) {
	inboundBefore := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V0")
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")

	vaults := []openapi.Vault{*openapi.NewVault(
		[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "1000000000000"}},
		thorchain.ActiveVault.String(),
		nil,
		[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inboundAfter.String()}},
	)}

	if !xmrVaultRotationReady(vaults, inboundBefore, inboundAfter, 2) {
		t.Fatal("expected XMR active vault to be ready")
	}
}

func TestInitialXMRVaultReadyWithoutPreviousInbound(t *testing.T) {
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")
	vaults := []openapi.Vault{*openapi.NewVault(
		[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "1000000000000"}},
		thorchain.ActiveVault.String(),
		nil,
		[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inboundAfter.String()}},
	)}

	if !xmrVaultRotationReady(vaults, common.NoAddress, inboundAfter, 2) {
		t.Fatal("expected initial funded XMR vault to be ready without a previous inbound address")
	}
}

func TestXMRActiveVaultReadyRequiresFundedInboundVault(t *testing.T) {
	inboundBefore := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V0")
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")

	vaults := []openapi.Vault{*openapi.NewVault(
		[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "0"}},
		thorchain.ActiveVault.String(),
		nil,
		[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inboundAfter.String()}},
	)}

	if xmrVaultRotationReady(vaults, inboundBefore, inboundAfter, 2) {
		t.Fatal("expected zero-balance XMR active vault to be unready")
	}
}

func TestXMRActiveVaultReadyRequiresMatchingActiveInboundVault(t *testing.T) {
	inboundBefore := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V0")
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")

	vaults := []openapi.Vault{
		*openapi.NewVault(
			[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "1000000000000"}},
			thorchain.RetiringVault.String(),
			nil,
			[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inboundAfter.String()}},
		),
		*openapi.NewVault(
			[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "1000000000000"}},
			thorchain.ActiveVault.String(),
			nil,
			[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inboundBefore.String()}},
		),
	}

	if xmrVaultRotationReady(vaults, inboundBefore, inboundAfter, 2) {
		t.Fatal("expected only the rotated active XMR vault to satisfy readiness")
	}
}

func TestXMRPostChurnReadyRequiresSpendableRotatedVault(t *testing.T) {
	inboundBefore := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V0")
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")

	vaults := []openapi.Vault{*openapi.NewVault(
		[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "1000000000000"}},
		thorchain.ActiveVault.String(),
		nil,
		[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inboundAfter.String()}},
	)}
	snapshot := xmrVaultBalanceSnapshot{spendable: sdkmath.NewUint(1)}

	if !xmrPostChurnReady(vaults, inboundBefore, inboundAfter, 2, snapshot) {
		t.Fatal("expected spendable rotated XMR active vault to be ready")
	}

	snapshot.spendable = sdkmath.ZeroUint()
	if xmrPostChurnReady(vaults, inboundBefore, inboundAfter, 2, snapshot) {
		t.Fatal("expected zero-spendable XMR active vault to be unready")
	}
}

func TestXMRPostChurnIntervalSchedulesNearFutureChurn(t *testing.T) {
	interval := xmrPostChurnScheduledInterval(522, 1)
	if interval != 531 {
		t.Fatalf("expected interval 531 to schedule churn at height 532, got %d", interval)
	}
}

func TestXMRPostChurnIntervalHandlesFreshVault(t *testing.T) {
	interval := xmrPostChurnScheduledInterval(10, 12)
	if interval != 10 {
		t.Fatalf("expected minimum near-future interval, got %d", interval)
	}
}

func TestXMRPostChurnSchedulesFromAPIVaultHeights(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		height int64
		found  bool
	}{
		{"genesis omits zero status height", `[{"status":"ActiveVault"}]`, 0, true},
		{"latest active height", `[{"status":"ActiveVault","status_since":90},{"status":"ActiveVault"},{"status":"ActiveVault","status_since":120},{"status":"RetiringVault","status_since":130}]`, 120, true},
		{"no active vault", `[{"status":"RetiringVault","status_since":130}]`, 0, false},
		{"empty response", `[]`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var vaults []openapi.Vault
			if err := json.Unmarshal([]byte(tc.body), &vaults); err != nil {
				t.Fatal(err)
			}
			height, found := lastActiveVaultHeight(vaults)
			if height != tc.height || found != tc.found {
				t.Fatalf("got height=%d found=%t, want height=%d found=%t", height, found, tc.height, tc.found)
			}
		})
	}
}

func TestXMRChurnOutRequiresExistingInboundSnapshot(t *testing.T) {
	for _, requireExisting := range []bool{false, true} {
		a := newXMRPostChurnActor(rand.New(rand.NewPCG(1, 2)))
		a.requireExisting = requireExisting
		for _, tc := range []struct {
			name         string
			queryErr     error
			allowInitial bool
		}{
			{"empty address", nil, true},
			{"confirmed absence", thornode.ErrInboundAddressNotFound, true},
			{"wrapped absence", fmt.Errorf("XMR: %w", thornode.ErrInboundAddressNotFound), true},
			{"unclassified error", errors.New("no inbound address found for chain XMR"), false},
			{"failed query", errors.New("temporarily unavailable"), false},
		} {
			result := a.captureInbound(func(common.Chain) (common.Address, *common.Address, error) {
				return common.NoAddress, nil, tc.queryErr
			})
			wantContinue := tc.allowInitial && !requireExisting
			if result.Continue != wantContinue || result.Finish {
				t.Fatalf("%s: requireExisting=%v, result=%+v", tc.name, requireExisting, result)
			}
		}
		before := common.Address("existing-XMR-vault")
		result := a.captureInbound(func(common.Chain) (common.Address, *common.Address, error) {
			return before, nil, nil
		})
		if !result.Continue || !a.inboundBefore.Equals(before) {
			t.Fatal("existing vault address was not captured for later rotation comparison")
		}
	}
}

func TestXMRChurnOutVerificationDoesNotStartAnotherChurn(t *testing.T) {
	before, after := NewXMRChurnOutChecks(rand.New(rand.NewPCG(1, 2)))
	if len(before.Ops) != 1 || !strings.Contains(before.Ops[0].String(), "captureActiveInbound") {
		t.Fatal("expected capture before churn-out")
	}
	if !strings.Contains(after.Ops[0].String(), "waitForVaultRotation") {
		t.Fatal("verification must wait for the caller's rotation")
	}
	for _, op := range after.Ops {
		if strings.Contains(op.String(), "startChurn") || strings.Contains(op.String(), "captureActiveInbound") {
			t.Fatal("verification must neither replace the saved address nor initiate a new churn")
		}
	}
}

func TestXMRChurnOutMatchesMemberIdentitiesNotOnlyCount(t *testing.T) {
	nodes := []openapi.Node{}
	for _, pubkey := range []string{"one", "two", "three", "replacement"} {
		node := openapi.Node{Status: "Active"}
		node.PubKeySet.SetSecp256k1(pubkey)
		nodes = append(nodes, node)
	}
	inactive := openapi.Node{Status: "Standby"}
	inactive.PubKeySet.SetSecp256k1("departed")
	nodes = append(nodes, inactive)
	address := common.Address("current-XMR-inbound")
	frost := "frost-pubkey"
	for _, tc := range []struct {
		name    string
		members []string
		ready   bool
	}{
		{"replacement set in any order", []string{"replacement", "three", "two", "one"}, true},
		{"departed member still present", []string{"one", "two", "three", "departed"}, false},
		{"initial single-node vault", []string{"one"}, false},
		{"missing member", []string{"one", "two", "three"}, false},
		{"duplicate member", []string{"one", "two", "three", "three"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := openapi.Vault{
				Status: "ActiveVault", PubKeyFrost: &frost, Membership: tc.members,
				Addresses: []openapi.VaultAddress{{Chain: "XMR", Address: address.String()}},
			}
			if got := xmrVaultMatchesActiveNodes([]openapi.Vault{vault}, address, nodes); got != tc.ready {
				t.Fatalf("membership readiness = %v, want %v", got, tc.ready)
			}
		})
	}
}

func TestXMRPostChurnRetriesFailedBaselineLookup(t *testing.T) {
	a := newXMRPostChurnActor(rand.New(rand.NewPCG(1, 2)))
	result := a.captureInbound(func(common.Chain) (common.Address, *common.Address, error) {
		return common.NoAddress, nil, errors.New("temporarily unavailable")
	})
	if result.Continue || result.Finish || !a.inboundBefore.IsEmpty() {
		t.Fatalf("failed lookup must retry without advancing: result=%+v, baseline=%s", result, a.inboundBefore)
	}

	inbound := common.Address("existing-XMR-vault")
	result = a.captureInbound(func(common.Chain) (common.Address, *common.Address, error) {
		return inbound, nil, nil
	})
	if !result.Continue || !a.inboundBefore.Equals(inbound) {
		t.Fatal("successful retry must capture the existing vault")
	}
	vaults := []openapi.Vault{*openapi.NewVault(
		[]openapi.Coin{{Asset: common.XMRAsset.String(), Amount: "1000000000000"}},
		thorchain.ActiveVault.String(),
		nil,
		[]openapi.VaultAddress{{Chain: common.XMRChain.String(), Address: inbound.String()}},
	)}
	if xmrVaultRotationReady(vaults, a.inboundBefore, inbound, 4) {
		t.Fatal("the unchanged funded vault must not count as a rotation after a lookup failure")
	}
}
