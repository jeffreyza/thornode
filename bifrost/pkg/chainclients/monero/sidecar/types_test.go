package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestBroadcastReqJSONIncludesRequiredFields(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(BroadcastReq{})
	if err != nil {
		t.Fatalf("marshal broadcast request: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal broadcast request map: %v", err)
	}
	if _, ok := decoded["vault_pub_key"]; !ok {
		t.Fatalf("expected vault_pub_key field in broadcast request JSON: %s", string(raw))
	}
	if _, ok := decoded["msg_id"]; !ok {
		t.Fatalf("expected msg_id field in broadcast request JSON: %s", string(raw))
	}
}

func TestBalanceIsReconciled(t *testing.T) {
	reconciled := true
	unreconciled := false

	tests := []struct {
		name       string
		state      string
		reconciled *bool
		pending    int
		present    bool
		want       bool
	}{
		{name: "missing state fails closed", want: false},
		{name: "reconciled without pending count fails closed", state: "reconciled", reconciled: &reconciled, want: false},
		{name: "explicit reconciled", state: "reconciled", reconciled: &reconciled, present: true, want: true},
		{name: "v2 enum overrides stale legacy bool", state: "reconciled", reconciled: &unreconciled, present: true, want: true},
		{name: "legacy bool without state fails closed", reconciled: &reconciled, want: false},
		{name: "explicit false", state: "pending_outgoing", reconciled: &unreconciled, present: true, want: false},
		{name: "inconsistent pending count", state: "reconciled", reconciled: &reconciled, pending: 1, present: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pending *int
			if tt.present {
				pending = &tt.pending
			}
			if got := (Balance{
				ReconciliationState:  tt.state,
				Reconciled:           tt.reconciled,
				PendingOutgoingCount: pending,
			}).IsReconciled(); got != tt.want {
				t.Fatalf("IsReconciled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBalanceDecodesNonSpendableQuarantineCount(t *testing.T) {
	t.Parallel()

	var balance Balance
	if err := json.Unmarshal([]byte(`{
		"balance":1000,
		"unlocked_balance":1000,
		"spendable_balance":400,
		"reconciliation_state":"reconciled",
		"pending_outgoing_count":0,
		"quarantined_outgoing_count":2
	}`), &balance); err != nil {
		t.Fatalf("decode balance: %v", err)
	}
	if balance.QuarantinedOutgoingCount == nil || *balance.QuarantinedOutgoingCount != 2 {
		t.Fatalf("unexpected quarantine count: %v", balance.QuarantinedOutgoingCount)
	}
	if !balance.IsReconciled() {
		t.Fatal("non-spendable quarantine should not perpetuate solvency deferral")
	}
}

func TestRegisterVaultRespDecodesContinuity(t *testing.T) {
	t.Parallel()

	var resp RegisterVaultResp
	if err := json.Unmarshal([]byte(`{
		"registered": true,
		"effective_birthday": 40,
		"reset_epoch": 3,
		"expected_next_height": 42
	}`), &resp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if !resp.Registered || resp.EffectiveBirthday != 40 || resp.ResetEpoch != 3 ||
		resp.ExpectedNextHeight == nil || *resp.ExpectedNextHeight != 42 {
		t.Fatalf("unexpected register continuity: %+v", resp)
	}
}

func TestRegisterVaultRespRejectsMissingRequiredContinuity(t *testing.T) {
	t.Parallel()

	var resp RegisterVaultResp
	if err := json.Unmarshal([]byte(`{"registered":true,"effective_birthday":40}`), &resp); err == nil {
		t.Fatal("expected missing reset_epoch to fail closed")
	}
}

func TestRegisterVaultRespRejectsMissingEffectiveBirthday(t *testing.T) {
	t.Parallel()

	var resp RegisterVaultResp
	if err := json.Unmarshal([]byte(`{
		"registered":true,
		"reset_epoch":0,
		"expected_next_height":42
	}`), &resp); err == nil {
		t.Fatal("expected missing effective_birthday to fail closed")
	}
}

func TestScannerRewindRequiredErrorSupportsErrorsAs(t *testing.T) {
	t.Parallel()

	want := &ScannerRewindRequiredError{
		RequestedHeight: 50,
		NextHeight:      40,
		ResetEpoch:      7,
	}
	err := fmt.Errorf("sync failed: %w", want)

	var got *ScannerRewindRequiredError
	if !errors.As(err, &got) {
		t.Fatalf("expected errors.As to find typed rewind in %v", err)
	}
	if got != want {
		t.Fatalf("errors.As returned %+v, want %+v", got, want)
	}
}
