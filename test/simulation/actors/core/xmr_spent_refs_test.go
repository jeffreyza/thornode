package core

import (
	"encoding/json"
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestXMRSpentRefsRequireTheNewOutboundsStoredEvidence(t *testing.T) {
	for _, scenario := range []string{
		"matching new refs", "historical refs only", "unrelated new refs", "no stored refs",
		"missing observed refs", "different observed refs", "observation pending", "missing observed tx",
		"late final observation", "late final without refs", "late final reused input",
		"missing consensus height", "negative consensus height", "reverted voter",
		"observation reverted", "unknown status", "wrong observed tx", "wrong chain", "wrong vault",
		"missing key image", "missing source tx", "reused input", "missing snapshot", "duplicate reference",
	} {
		t.Run(scenario, func(t *testing.T) {
			outbound := strings.Repeat("b", 64)
			old := common.OutputRef{TxHash: strings.Repeat("a", 64), OutputIndex: 0, KeyImage: strings.Repeat("c", 64), SpendTxHash: strings.Repeat("d", 64)}
			first := common.OutputRef{TxHash: old.TxHash, OutputIndex: 1, KeyImage: strings.Repeat("e", 64), SpendTxHash: outbound}
			second := common.OutputRef{TxHash: strings.Repeat("f", 64), OutputIndex: 0, KeyImage: strings.Repeat("1", 64), SpendTxHash: outbound}
			a := &XMRSpentRefVerifyActor{Actor: *NewActor("spent refs", rand.New(rand.NewPCG(1, 2))), outboundTxID: outbound}
			a.captureSpentRefs("frost-vault", []common.OutputRef{old})
			observed := thornode.ObservedTxSpentRefs{ConsensusHeight: 718}
			observed.ObservedTx.Tx = openapi.Tx{Id: &outbound, Chain: openapi.PtrString("XMR")}
			observed.ObservedTx.Status = "done"
			observed.ObservedTx.ObservedPubKey = "frost-vault"
			observed.ObservedTx.SpentOutputRefs = []common.OutputRef{first, second}
			stored := []common.OutputRef{old, first, second}
			wantError := false
			wantPending := false
			switch scenario {
			case "matching new refs":
				// Hash case, order and a zero output index must not change identity.
				stored = []common.OutputRef{second, old, first}
				stored[2].TxHash = strings.ToUpper(first.TxHash)
				stored[2].KeyImage = strings.ToUpper(first.KeyImage)
				stored[2].SpendTxHash = strings.ToUpper(first.SpendTxHash)
			case "historical refs only":
				stored = []common.OutputRef{old}
				wantPending = true
			case "unrelated new refs":
				stored[1].SpendTxHash = old.SpendTxHash
				stored[2].SpendTxHash = old.SpendTxHash
				wantPending = true
			case "no stored refs":
				stored = nil
				wantPending = true
			case "missing observed refs":
				// The selected consensus observation may come from a non-signer.
				// Its missing refs must not hide separately approved, stored refs.
				observed.ObservedTx.SpentOutputRefs = nil
			case "different observed refs":
				// Only the stored set has passed the separate spent-ref quorum.
				observed.ObservedTx.SpentOutputRefs = []common.OutputRef{first}
				observed.ObservedTx.SpentOutputRefs[0].KeyImage = old.KeyImage
			case "late final observation":
				// castObservedTx maps incomplete to an empty status. Exercise the
				// generated response type's omission and the simulation's decoder.
				response := thortypes.QueryObservedTxVoter{
					Height: observed.ConsensusHeight,
					Tx: thortypes.QueryObservedTx{
						Tx:             common.Tx{ID: common.TxID(outbound), Chain: common.XMRChain},
						ObservedPubKey: common.PubKey(a.vaultPubKey),
					},
				}
				payload, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(payload), `"status"`) {
					t.Fatalf("incomplete observation must omit status: %s", payload)
				}
				observed = thornode.ObservedTxSpentRefs{}
				if err := json.Unmarshal(payload, &observed); err != nil {
					t.Fatal(err)
				}
			case "late final without refs":
				observed.ObservedTx.Status = ""
				stored = nil
				wantPending = true
			case "late final reused input":
				observed.ObservedTx.Status = ""
				stored[1].OutputIndex = old.OutputIndex
				wantError = true
			case "missing consensus height":
				observed.ConsensusHeight = 0
				wantPending = true
			case "negative consensus height":
				observed.ConsensusHeight = -1
				observed.ObservedTx.Status = ""
				wantPending = true
			case "reverted voter":
				observed.Reverted = true
				observed.ObservedTx.Status = ""
				wantPending = true
			case "observation pending":
				observed.ConsensusHeight = 0
				observed.ObservedTx.Status = ""
				wantPending = true
			case "observation reverted":
				observed.ObservedTx.Status = "reverted"
				wantPending = true
			case "unknown status":
				observed.ObservedTx.Status = "unknown"
				wantPending = true
			case "missing observed tx":
				observed.ObservedTx.Tx.Id = nil
				wantPending = true
			case "wrong observed tx":
				observed.ObservedTx.Tx.Id = openapi.PtrString(old.SpendTxHash)
				wantError = true
			case "wrong chain":
				observed.ObservedTx.Tx.Chain = openapi.PtrString("BTC")
				wantError = true
			case "wrong vault":
				observed.ObservedTx.ObservedPubKey = "other-vault"
				wantError = true
			case "missing key image":
				stored[1].KeyImage = ""
				wantError = true
			case "missing source tx":
				stored[1].TxHash = ""
				wantError = true
			case "reused input":
				stored[1].TxHash = strings.ToUpper(old.TxHash)
				stored[1].OutputIndex = old.OutputIndex
				wantError = true
			case "missing snapshot":
				a.spentBefore = nil
				wantError = true
			case "duplicate reference":
				stored = []common.OutputRef{old, first, first}
				wantError = true
			}
			completed := false
			a.Timeout = 2 * time.Millisecond
			a.Interval = time.Millisecond
			a.Ops = []Op{
				func(_ *OpConfig) OpResult { return a.verifyOutboundSpentRefs(observed, stored) },
				func(_ *OpConfig) OpResult { completed = true; return OpResult{Finish: true} },
			}
			err := a.Execute(nil)
			if wantPending {
				if !errors.Is(err, ErrTimeout) || completed {
					t.Fatalf("incomplete evidence passed: completed=%v err=%v", completed, err)
				}
			} else if wantError {
				if err == nil || errors.Is(err, ErrTimeout) || completed {
					t.Fatalf("invalid evidence was not rejected: completed=%v err=%v", completed, err)
				}
			} else if err != nil || !completed {
				t.Fatalf("valid new outbound evidence failed: completed=%v err=%v", completed, err)
			}
		})
	}
}

func TestXMRSpentRefsWaitForSwapOutbound(t *testing.T) {
	for _, tc := range []struct {
		name, details                                         string
		pending, queryFailure, stagesFailure, terminal, ready bool
	}{
		{name: "no outbound", details: `{}`, terminal: true},
		{name: "multiple outbounds", details: `{"out_txs":[{"id":"one","chain":"XMR"},{"id":"two","chain":"XMR"}]}`, terminal: true},
		{name: "wrong chain", details: `{"out_txs":[{"id":"outbound","chain":"BTC"}]}`, terminal: true},
		{name: "missing ID", details: `{"out_txs":[{"chain":"XMR"}]}`, terminal: true},
		{name: "empty ID", details: `{"out_txs":[{"id":"","chain":"XMR"}]}`, terminal: true},
		{name: "pending", details: `{}`, pending: true},
		{name: "stage query failure", stagesFailure: true},
		{name: "details query failure", queryFailure: true},
		{name: "refunded", details: `{"actions":[{"memo":"REFUND:inbound"}]}`, terminal: true},
		{name: "identified outbound", details: `{"out_txs":[{"id":"outbound","chain":"XMR"}]}`, ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			a := &XMRSpentRefVerifyActor{
				Actor:    *NewActor("spent refs", rand.New(rand.NewPCG(1, 2))),
				swapTxID: "inbound",
				getSwapStages: func(txID string) (openapi.TxStagesResponse, error) {
					calls = append(calls, "stages:"+txID)
					if tc.stagesFailure {
						return openapi.TxStagesResponse{}, errors.New("unavailable")
					}
					return openapi.TxStagesResponse{OutboundSigned: &openapi.OutboundSignedStage{Completed: !tc.pending}}, nil
				},
				getSwapDetails: func(txID string) (openapi.TxDetailsResponse, error) {
					calls = append(calls, "details:"+txID)
					var details openapi.TxDetailsResponse
					if tc.queryFailure {
						return details, errors.New("unavailable")
					}
					if err := json.Unmarshal([]byte(tc.details), &details); err != nil {
						t.Fatal(err)
					}
					return details, nil
				},
			}
			result := a.waitForSwapOutbound(nil)
			if result.Continue != tc.ready || result.Finish != tc.terminal || (result.Error != nil) != tc.terminal {
				t.Fatalf("unexpected outbound validation: %+v", result)
			}
			wantID := ""
			if tc.ready {
				wantID = "outbound"
			}
			if a.outboundTxID != wantID {
				t.Fatalf("recorded outbound %q, want %q", a.outboundTxID, wantID)
			}
			wantCalls := "stages:inbound,details:inbound"
			if tc.stagesFailure {
				wantCalls = "stages:inbound"
			}
			if strings.Join(calls, ",") != wantCalls {
				t.Fatalf("outbound validation must reuse the gate's details: %v", calls)
			}
		})
	}
}
