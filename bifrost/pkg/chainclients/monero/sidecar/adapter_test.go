package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestFrostAdapterTranslation(t *testing.T) {
	t.Parallel()

	var gotPrepare SignPrepareReq
	var gotPartial SignPartialReq
	var gotCombine SignCombineReq

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/sign/prepare":
			if err := json.NewDecoder(r.Body).Decode(&gotPrepare); err != nil {
				t.Fatalf("decode prepare request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(SignPrepareResp{
				SessionID:      "session-1",
				Preprocess:     "preprocess-hex",
				SelectedInputs: []SpentRef{{TxHash: strings.Repeat("a", 64), OutputIndex: 3}},
			})
		case "/v1/monero/sign/partial":
			if err := json.NewDecoder(r.Body).Decode(&gotPartial); err != nil {
				t.Fatalf("decode partial request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(SignPartialResp{
				SignatureShare: "share-hex",
			})
		case "/v1/monero/sign/combine":
			if err := json.NewDecoder(r.Body).Decode(&gotCombine); err != nil {
				t.Fatalf("decode combine request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(SignCombineResp{
				TxHash:    "tx-hash",
				TxHex:     "tx-hex",
				TxKey:     strings.Repeat("1", 64),
				FeeAtomic: 42,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	adapter := NewFrostAdapter(signer)

	prepareReq := tssp.FrostSidecarPrepareReq{
		VaultPubKey:                 "vault-pk",
		ToAddress:                   "48abc",
		AmountAtomic:                1000,
		Memo:                        "OUT:1",
		FeeRate:                     7,
		DecoyContext:                "ctx-123",
		MaxSelectedInputValueAtomic: 123456789,
		MaxGasAtomic:                987654321,
	}
	prepareResp, err := adapter.FrostSignPrepare(prepareReq)
	if err != nil {
		t.Fatalf("adapter prepare: %v", err)
	}
	if prepareResp.SessionID != "session-1" || prepareResp.Preprocess != "preprocess-hex" {
		t.Fatalf("unexpected prepare response: %+v", prepareResp)
	}
	if len(prepareResp.SelectedInputs) != 1 ||
		prepareResp.SelectedInputs[0].TxHash != strings.Repeat("a", 64) ||
		prepareResp.SelectedInputs[0].OutputIndex != 3 {
		t.Fatalf("selected input response translation mismatch: %+v", prepareResp.SelectedInputs)
	}
	if gotPrepare.VaultPubKey != prepareReq.VaultPubKey ||
		gotPrepare.ToAddress != prepareReq.ToAddress ||
		gotPrepare.AmountAtomic != prepareReq.AmountAtomic ||
		gotPrepare.Memo != prepareReq.Memo ||
		gotPrepare.FeeRate != prepareReq.FeeRate ||
		gotPrepare.DecoyContext != prepareReq.DecoyContext ||
		gotPrepare.MaxSelectedInputValueAtomic != prepareReq.MaxSelectedInputValueAtomic ||
		gotPrepare.MaxGasAtomic != prepareReq.MaxGasAtomic {
		t.Fatalf("prepare request translation mismatch: %+v", gotPrepare)
	}

	partialReq := tssp.FrostSidecarPartialReq{
		SessionID: "session-1",
		AllPreprocesses: map[string]string{
			"1": "aa",
			"2": "bb",
		},
	}
	partialResp, err := adapter.FrostSignPartial(partialReq)
	if err != nil {
		t.Fatalf("adapter partial: %v", err)
	}
	if partialResp.SignatureShare != "share-hex" {
		t.Fatalf("unexpected partial response: %+v", partialResp)
	}
	if gotPartial.SessionID != partialReq.SessionID || !reflect.DeepEqual(gotPartial.AllPreprocesses, partialReq.AllPreprocesses) {
		t.Fatalf("partial request translation mismatch: %+v", gotPartial)
	}

	combineReq := tssp.FrostSidecarCombineReq{
		SessionID: "session-1",
		AllShares: map[string]string{
			"1": "cc",
			"2": "dd",
		},
	}
	combineResp, err := adapter.FrostSignCombine(combineReq)
	if err != nil {
		t.Fatalf("adapter combine: %v", err)
	}
	if combineResp.TxHash != "tx-hash" || combineResp.TxHex != "tx-hex" ||
		combineResp.TxKey != strings.Repeat("1", 64) || combineResp.FeeAtomic != 42 {
		t.Fatalf("unexpected combine response: %+v", combineResp)
	}
	if gotCombine.SessionID != combineReq.SessionID || !reflect.DeepEqual(gotCombine.AllShares, combineReq.AllShares) {
		t.Fatalf("combine request translation mismatch: %+v", gotCombine)
	}
}

func TestFrostAdapterDKGFinalize(t *testing.T) {
	t.Parallel()

	var gotFinalize DKGFinalizeReq

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/dkg/finalize":
			if err := json.NewDecoder(r.Body).Decode(&gotFinalize); err != nil {
				t.Fatalf("decode finalize request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(DKGFinalizeResp{
				Success:     true,
				KeyBytesHex: "deadbeef0123",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	adapter := NewFrostAdapter(signer)

	finalizeReq := tssp.FrostSidecarDKGFinalizeReq{
		SessionID: "sess-1",
		GroupKey:  "aabbccdd",
		Address:   "4Addr",
	}
	finalizeResp, err := adapter.FrostDKGFinalize(finalizeReq)
	if err != nil {
		t.Fatalf("adapter finalize: %v", err)
	}
	if !finalizeResp.Success {
		t.Fatalf("expected success=true")
	}
	if finalizeResp.KeyBytesHex != "deadbeef0123" {
		t.Fatalf("expected key_bytes_hex=deadbeef0123, got %s", finalizeResp.KeyBytesHex)
	}
	if gotFinalize.SessionID != "sess-1" || gotFinalize.GroupKey != "aabbccdd" || gotFinalize.Address != "4Addr" {
		t.Fatalf("finalize request translation mismatch: %+v", gotFinalize)
	}
}

func TestFrostAdapterAllowsEmptyDKGRound2SharesForSingleSigner(t *testing.T) {
	t.Parallel()

	var gotRound2 DKGRound2Req

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/dkg/round2":
			if err := json.NewDecoder(r.Body).Decode(&gotRound2); err != nil {
				t.Fatalf("decode round2 request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(DKGRound2Resp{
				EncryptedShares: map[string]string{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	adapter := NewFrostAdapter(signer)

	resp, err := adapter.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{
		SessionID: "sess-1",
		AllCommitments: map[string]string{
			"1": "commit-1",
		},
	})
	if err != nil {
		t.Fatalf("adapter round2 single signer: %v", err)
	}
	if len(resp.EncryptedShares) != 0 {
		t.Fatalf("expected empty encrypted shares for single signer dkg, got %+v", resp.EncryptedShares)
	}
	if gotRound2.SessionID != "sess-1" || !reflect.DeepEqual(gotRound2.AllCommitments, map[string]string{"1": "commit-1"}) {
		t.Fatalf("round2 request translation mismatch: %+v", gotRound2)
	}
}

func TestFrostAdapterRejectsEmptyDKGRound2SharesForMultiSigner(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/dkg/round2":
			_ = json.NewEncoder(w).Encode(DKGRound2Resp{
				EncryptedShares: map[string]string{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	adapter := NewFrostAdapter(signer)

	_, err = adapter.FrostDKGRound2(tssp.FrostSidecarDKGRound2Req{
		SessionID: "sess-2",
		AllCommitments: map[string]string{
			"1": "commit-1",
			"2": "commit-2",
		},
	})
	if err == nil {
		t.Fatal("expected multi-signer round2 to reject empty encrypted shares")
	}
}
