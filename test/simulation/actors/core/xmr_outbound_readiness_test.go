package core

import (
	"encoding/json"
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type xmrFeeBridge struct {
	thorclient.ThorchainBridge
	size, rate uint64
	err        error
	calls      int
}

func (b *xmrFeeBridge) GetNetworkFee(chain common.Chain) (uint64, uint64, error) {
	if !chain.Equals(common.XMRChain) {
		panic("expected XMR network fee query")
	}
	b.calls++
	return b.size, b.rate, b.err
}

func TestXMRNetworkFeeReadiness(t *testing.T) {
	for _, tc := range []struct {
		name       string
		size, rate uint64
		err        error
		ready      bool
	}{
		{name: "missing"},
		{name: "size zero", rate: 1200000},
		{name: "rate zero", size: 10000},
		{name: "query failure", size: 10000, rate: 1200000, err: errors.New("unavailable")},
		{name: "committed", size: 10000, rate: 1200000, ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &xmrFeeBridge{size: tc.size, rate: tc.rate, err: tc.err}
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			logger := zerolog.Nop()
			result := waitForXMRNetworkFee(&logger, config)
			if result.Continue != tc.ready || result.Finish || result.Error != nil {
				t.Fatalf("unexpected readiness result: %+v", result)
			}
		})
	}
}

func TestXMRActorsWaitBeforeDepositingOrAcquiringUsers(t *testing.T) {
	bridge := &xmrFeeBridge{}
	config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
	rng := rand.New(rand.NewPCG(1, 2))
	spent := &XMRSpentRefVerifyActor{Actor: *NewActor("spent", rng)}
	swap := &SwapActor{Actor: *NewActor("swap", rng), from: common.BTCAsset, to: common.XMRAsset}
	for _, op := range []Op{spent.swapToXMR, swap.acquireUser} {
		before := bridge.calls
		result := op(config)
		if result.Continue || result.Finish || result.Error != nil || bridge.calls != before+1 {
			t.Fatalf("actor did not wait at the fee gate: %+v", result)
		}
	}
	// An accepted deposit must not be submitted again, even if the fee query
	// later fails. No clients are provided, so proceeding to a deposit fails.
	spent.swapTxID = "already-submitted"
	before := bridge.calls
	if result := spent.swapToXMR(nil); !result.Continue || bridge.calls != before {
		t.Fatalf("accepted deposit was not preserved: %+v", result)
	}
}

func TestXMRNetworkFeeDelayIsBoundedByActorDeadline(t *testing.T) {
	bridge := &xmrFeeBridge{}
	config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
	a := NewActor("fee deadline", rand.New(rand.NewPCG(1, 2)))
	a.Timeout = time.Nanosecond
	a.Ops = []Op{func(c *OpConfig) OpResult { return waitForXMRNetworkFee(a.Log(), c) }}
	if err := a.Execute(config); !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected actor timeout for missing fee, got %v", err)
	}
	bridge.size, bridge.rate = 10000, 1200000
	if err := a.Execute(config); err != nil {
		t.Fatalf("committed fee should allow progress: %v", err)
	}
}

func TestXMRSwapFailureDistinguishesPendingAndTerminalOutbounds(t *testing.T) {
	for _, tc := range []struct {
		name, stages, details, want string
	}{
		{"not observed", `{}`, `{}`, ""},
		{"missing swap status", `{"swap_finalised":{"completed":true}}`, `{"finalised_height":364}`, ""},
		{"pending swap", `{"swap_finalised":{"completed":false},"swap_status":{"pending":true}}`, `{"finalised_height":364}`, ""},
		{"streaming", `{"swap_finalised":{"completed":true},"swap_status":{"pending":true}}`, `{"finalised_height":364}`, ""},
		{"not finalized", `{"swap_finalised":{"completed":true},"swap_status":{"pending":false}}`, `{}`, ""},
		{"scheduling failed", `{"swap_finalised":{"completed":true},"swap_status":{"pending":false}}`, `{"finalised_height":364}`, "finalized without a scheduled outbound"},
		{"scheduled unsigned", `{"swap_finalised":{"completed":true},"swap_status":{"pending":false}}`, `{"finalised_height":364,"actions":[{"memo":"OUT:tx"}]}`, ""},
		{"outbound observed", `{"swap_finalised":{"completed":true},"swap_status":{"pending":false}}`, `{"finalised_height":364,"out_txs":[{}]}`, ""},
		{"reverted", `{}`, `{"reverted":true}`, "reverted"},
		{"refunded", `{}`, `{"actions":[{"memo":"REFUND:tx"}]}`, "refunded"},
		{"original refund memo", `{}`, `{"actions":[{"original_memo":"refund:tx"}]}`, "refunded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stages openapi.TxStagesResponse
			var details openapi.TxDetailsResponse
			if err := json.Unmarshal([]byte(tc.stages), &stages); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.details), &details); err != nil {
				t.Fatal(err)
			}
			err := xmrSwapFailure("incident-tx", stages, details)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "incident-tx") {
				t.Fatalf("expected %q with transaction ID, got %v", tc.want, err)
			}
		})
	}
}

func TestWaitForXMRSwapOutbound(t *testing.T) {
	for _, tc := range []struct {
		name, stages, details, wantError string
		stagesFail, detailsFail, ready   bool
	}{
		{name: "stages unavailable", stagesFail: true},
		{name: "details unavailable", stages: `{}`, detailsFail: true},
		{name: "not observed", stages: `{}`, details: `{}`},
		{name: "unsigned", stages: `{"outbound_signed":{"completed":false}}`, details: `{}`},
		{name: "signed", stages: `{"outbound_signed":{"completed":true}}`, details: `{"out_txs":[{"id":"outbound","chain":"XMR"}]}`, ready: true},
		{name: "refunded", stages: `{"outbound_signed":{"completed":true}}`, details: `{"actions":[{"memo":"REFUND:inbound"}]}`, wantError: "refunded"},
		{name: "reverted", stages: `{}`, details: `{"reverted":true}`, wantError: "reverted"},
		{name: "scheduling failed", stages: `{"swap_finalised":{"completed":true},"swap_status":{"pending":false}}`, details: `{"finalised_height":364}`, wantError: "finalized without a scheduled outbound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			getStages := func(txID string) (openapi.TxStagesResponse, error) {
				calls = append(calls, "stages:"+txID)
				var stages openapi.TxStagesResponse
				if tc.stagesFail {
					return stages, errors.New("temporarily unavailable")
				}
				if err := json.Unmarshal([]byte(tc.stages), &stages); err != nil {
					t.Fatal(err)
				}
				return stages, nil
			}
			getDetails := func(txID string) (openapi.TxDetailsResponse, error) {
				calls = append(calls, "details:"+txID)
				var details openapi.TxDetailsResponse
				if tc.detailsFail {
					return details, errors.New("temporarily unavailable")
				}
				if err := json.Unmarshal([]byte(tc.details), &details); err != nil {
					t.Fatal(err)
				}
				return details, nil
			}
			logger := zerolog.Nop()
			details, result := waitForXMRSwapOutbound(&logger, "inbound", getStages, getDetails)
			wantCalls := "stages:inbound,details:inbound"
			if tc.stagesFail {
				wantCalls = "stages:inbound"
			}
			if strings.Join(calls, ",") != wantCalls {
				t.Fatalf("queries must use one stages-then-details snapshot: %v", calls)
			}
			if result.Continue != tc.ready || result.Finish != (tc.wantError != "") {
				t.Fatalf("unexpected gate result: %+v", result)
			}
			if tc.wantError == "" {
				if result.Error != nil {
					t.Fatal(result.Error)
				}
			} else if result.Error == nil || !strings.Contains(result.Error.Error(), tc.wantError) || !strings.Contains(result.Error.Error(), "inbound") {
				t.Fatalf("expected %q with inbound ID, got %v", tc.wantError, result.Error)
			}
			if tc.ready && (len(details.OutTxs) != 1 || details.OutTxs[0].GetId() != "outbound") {
				t.Fatalf("ready gate must return the queried outbound details: %+v", details)
			}
		})
	}
}
