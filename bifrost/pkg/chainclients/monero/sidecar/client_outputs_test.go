package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestClientOutputs(t *testing.T) {
	t.Parallel()

	expectedPath := "/v1/monero/outputs/thorpub1vault%2Fwith%3Fchars"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != expectedPath {
			http.Error(w, "unexpected path: "+r.URL.EscapedPath(), http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(Outputs{
			Outputs: []Output{
				{
					TxHash:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					OutputIndex:     3,
					Amount:          12345,
					Height:          77,
					KeyImage:        "bbbb",
					Spent:           false,
					SubaddressMajor: 2,
					SubaddressMinor: 0,
				},
			},
		})
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	outputs, err := signer.Outputs("thorpub1vault/with?chars")
	if err != nil {
		t.Fatalf("GetOutputs failed: %v", err)
	}
	if len(outputs) != 1 {
		t.Fatalf("expected one output, got %d", len(outputs))
	}
	if outputs[0].OutputIndex != 3 || outputs[0].Amount != 12345 || outputs[0].Height != 77 {
		t.Fatalf("unexpected output payload: %+v", outputs[0])
	}
}

func TestClientOutputsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.Outputs("thorpub1vault")
	if err == nil {
		t.Fatal("expected GetOutputs error")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientMarkObservedUsesCanonicalRequestAndReturnsProof(t *testing.T) {
	t.Parallel()

	txHash := strings.Repeat("a", 64)
	blockHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("c", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/outputs/mark-observed" {
			http.NotFound(w, r)
			return
		}
		var req MarkObservedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.VaultPubKey != "thorpub1vault" || req.TxHash != txHash || req.Height != 77 || req.BlockHash != blockHash {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(Transfer{
			TxID:        txHash,
			TxKey:       txKey,
			Address:     "destination",
			FromAddress: "source",
			VaultPubKey: "thorpub1vault",
			Amount:      123,
			Fee:         45,
			Height:      77,
			Origin:      OutgoingOriginChainCommitted,
			SpentOutputRefs: []SpentRef{{
				TxHash:      strings.Repeat("d", 64),
				OutputIndex: 2,
				KeyImage:    strings.Repeat("e", 64),
			}},
		})
	}))
	defer server.Close()

	signer, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	proof, err := signer.MarkObserved("thorpub1vault", strings.ToUpper(txHash), 77, strings.ToUpper(blockHash))
	if err != nil {
		t.Fatalf("mark observed: %v", err)
	}
	if proof.TxID != txHash || proof.TxKey != txKey || proof.Fee != 45 || len(proof.SpentOutputRefs) != 1 {
		t.Fatalf("unexpected proof: %+v", proof)
	}
}
