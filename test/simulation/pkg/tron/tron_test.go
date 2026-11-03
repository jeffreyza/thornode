package tron

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const tronTestMnemonic = "master master master master master master master master master master master master master master master master master master master master master master master notice"

func TestSignTxAllowsSlowCreateTransaction(t *testing.T) {
	if tronAPITimeout <= 2*time.Second {
		t.Fatalf("expected tron API timeout to exceed 2s, got %s", tronAPITimeout)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if r.URL.Path != "/wallet/createtransaction" {
			t.Fatalf("expected createtransaction path, got %s", r.URL.Path)
		}
		if !strings.Contains(string(body), `"owner_address"`) {
			t.Fatalf("expected transaction creation payload, got %s", body)
		}

		time.Sleep(2500 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"txID":"1111111111111111111111111111111111111111111111111111111111111111","raw_data":{"contract":[]}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.SignTx(newTestSimTx(client.address)); err != nil {
		t.Fatalf("expected slow TRON createtransaction to succeed, got %v", err)
	}
}

func TestSignTxSerializesConcurrentCreateTransactionRequests(t *testing.T) {
	var serverMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if r.URL.Path != "/wallet/createtransaction" {
			t.Fatalf("expected createtransaction path, got %s", r.URL.Path)
		}
		if !strings.Contains(string(body), `"owner_address"`) {
			t.Fatalf("expected transaction creation payload, got %s", body)
		}

		serverMu.Lock()
		defer serverMu.Unlock()

		time.Sleep(1500 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"txID":"2222222222222222222222222222222222222222222222222222222222222222","raw_data":{"contract":[]}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	const requestCount = 2
	errs := make(chan error, requestCount)
	start := make(chan struct{})
	for i := 0; i < requestCount; i++ {
		go func() {
			<-start
			_, err := client.SignTx(newTestSimTx(client.address))
			errs <- err
		}()
	}
	close(start)

	for i := 0; i < requestCount; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("expected queued TRON createtransaction requests to succeed, got %v", err)
		}
	}
}

func TestSignTxRetriesCreateTransactionUntilTxIDPresent(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if r.URL.Path != "/wallet/createtransaction" {
			t.Fatalf("expected createtransaction path, got %s", r.URL.Path)
		}
		if !strings.Contains(string(body), `"owner_address"`) {
			t.Fatalf("expected transaction creation payload, got %s", body)
		}

		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts < 3 {
			_, _ = w.Write([]byte(`{"raw_data":{"contract":[]}}`))
			return
		}

		_, _ = w.Write([]byte(`{"txID":"3333333333333333333333333333333333333333333333333333333333333333","raw_data":{"contract":[]}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.SignTx(newTestSimTx(client.address)); err != nil {
		t.Fatalf("expected TRON signer to recover from missing txID response, got %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 createtransaction attempts, got %d", attempts)
	}
}

func TestSignTxAllowsExtendedCreateTransactionWarmup(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if r.URL.Path != "/wallet/createtransaction" {
			t.Fatalf("expected createtransaction path, got %s", r.URL.Path)
		}
		if !strings.Contains(string(body), `"owner_address"`) {
			t.Fatalf("expected transaction creation payload, got %s", body)
		}

		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts <= 110 {
			_, _ = w.Write([]byte(`{"raw_data":{"contract":[]}}`))
			return
		}

		_, _ = w.Write([]byte(`{"txID":"4444444444444444444444444444444444444444444444444444444444444444","raw_data":{"contract":[]}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.SignTx(newTestSimTx(client.address)); err != nil {
		t.Fatalf("expected TRON signer to survive an extended empty-txID warmup, got %v", err)
	}
	if attempts != 111 {
		t.Fatalf("expected 111 createtransaction attempts, got %d", attempts)
	}
}

func TestSignTxRehashesCreateTransactionWhenTxIDMissingButRawDataPresent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if r.URL.Path != "/wallet/createtransaction" {
			t.Fatalf("expected createtransaction path, got %s", r.URL.Path)
		}
		if !strings.Contains(string(body), `"owner_address"`) {
			t.Fatalf("expected transaction creation payload, got %s", body)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "visible": true,
		  "raw_data": {
		    "data": "53494d554c4154494f4e3a6475636b",
		    "contract": [{
		      "parameter": {
		        "value": {
		          "amount": 10000000000,
		          "owner_address": "TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW",
		          "to_address": "TBSsYaU49XyXEcziLfZWppdJEF9DrtkjnD"
		        },
		        "type_url": "type.googleapis.com/protocol.TransferContract"
		      },
		      "type": "TransferContract"
		    }],
		    "ref_block_bytes": "0144",
		    "ref_block_hash": "65f79438cddad8fe",
		    "expiration": 1774192794000,
		    "timestamp": 1774195911139
		  },
		  "raw_data_hex": "0a020144220865f79438cddad8fe4090b386b1d133520f53494d554c4154494f4e3a6475636b5a69080112650a2d747970652e676f6f676c65617069732e636f6d2f70726f746f636f6c2e5472616e73666572436f6e747261637412340a1541ee4eaa642b992412f628ff4cec1c96cf2fd0ea4d121541103387fbc5f2354db5849aa6bb0ddba8f7539c9d1880c8afa02570e3d3c4b2d133"
		}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if _, err := client.SignTx(newTestSimTx(client.address)); err != nil {
		t.Fatalf("expected TRON signer to rehash txID from raw_data_hex when the node omits txID, got %v", err)
	}
}

func TestSignTxReturnsCreateTransactionErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		if r.URL.Path != "/wallet/createtransaction" {
			t.Fatalf("expected createtransaction path, got %s", r.URL.Path)
		}
		if !strings.Contains(string(body), `"owner_address"`) {
			t.Fatalf("expected transaction creation payload, got %s", body)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Error":"class org.tron.core.exception.ContractValidateException : Validate TransferContract error, balance is not sufficient."}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.SignTx(newTestSimTx(client.address))
	if err == nil {
		t.Fatal("expected TRON signer to surface createtransaction errors")
	}
	if !strings.Contains(err.Error(), "balance is not sufficient") {
		t.Fatalf("expected insufficient balance error, got %v", err)
	}
}

func TestBroadcastTxWaitsForTransactionToBeConfirmed(t *testing.T) {
	restore := overrideBroadcastObservationTimings(100*time.Millisecond, time.Millisecond)
	defer restore()

	getTransactionCalls := 0
	getTransactionInfoCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/wallet/broadcasttransaction":
			_, _ = w.Write([]byte(`{"result":true,"txid":"5555555555555555555555555555555555555555555555555555555555555555"}`))
		case "/wallet/gettransactionbyid":
			getTransactionCalls++
			if getTransactionCalls < 2 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{"txID":"5555555555555555555555555555555555555555555555555555555555555555","raw_data":{"contract":[]}}`))
		case "/wallet/gettransactioninfobyid":
			getTransactionInfoCalls++
			if getTransactionInfoCalls < 3 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"5555555555555555555555555555555555555555555555555555555555555555","blockNumber":123,"receipt":{"net_usage":277}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	txid, err := client.BroadcastTx([]byte(`{"txID":"5555555555555555555555555555555555555555555555555555555555555555","raw_data":{"contract":[]}}`))
	if err != nil {
		t.Fatalf("expected TRON broadcast to succeed after transaction becomes queryable, got %v", err)
	}
	if txid != "5555555555555555555555555555555555555555555555555555555555555555" {
		t.Fatalf("expected txid to be returned, got %s", txid)
	}
	if getTransactionInfoCalls != 3 {
		t.Fatalf("expected 3 gettransactioninfobyid attempts, got %d", getTransactionInfoCalls)
	}
	if getTransactionCalls == 0 {
		t.Fatal("expected gettransactionbyid to be queried while waiting for confirmation")
	}
}

func TestBroadcastTxReturnsRejectedBroadcastErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/broadcasttransaction" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":false,"txid":"6666666666666666666666666666666666666666666666666666666666666666","code":"SIGERROR","message":"rejected"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.BroadcastTx([]byte(`{"txID":"6666666666666666666666666666666666666666666666666666666666666666","raw_data":{"contract":[]}}`))
	if err == nil {
		t.Fatal("expected TRON broadcast rejection to surface as an error")
	}
	if !strings.Contains(err.Error(), "SIGERROR") {
		t.Fatalf("expected broadcast error to include TRON code, got %v", err)
	}
}

func TestBroadcastTxFailsWhenBroadcastedTransactionNeverMaterializes(t *testing.T) {
	restore := overrideBroadcastObservationTimings(25*time.Millisecond, time.Millisecond)
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/wallet/broadcasttransaction":
			_, _ = w.Write([]byte(`{"result":true,"txid":"7777777777777777777777777777777777777777777777777777777777777777"}`))
		case "/wallet/gettransactionbyid":
			_, _ = w.Write([]byte(`{}`))
		case "/wallet/gettransactioninfobyid":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.BroadcastTx([]byte(`{"txID":"7777777777777777777777777777777777777777777777777777777777777777","raw_data":{"contract":[]}}`))
	if err == nil {
		t.Fatal("expected TRON broadcast to fail when the tx never becomes queryable")
	}
	if !strings.Contains(err.Error(), "not observed on tron node") {
		t.Fatalf("expected missing-broadcast error, got %v", err)
	}
}

func TestBroadcastTxRebroadcastsUntilTransactionConfirms(t *testing.T) {
	restore := overrideBroadcastObservationTimings(30*time.Millisecond, time.Millisecond)
	defer restore()

	broadcastCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/wallet/broadcasttransaction":
			broadcastCalls++
			_, _ = w.Write([]byte(`{"result":true,"txid":"8888888888888888888888888888888888888888888888888888888888888888"}`))
		case "/wallet/gettransactionbyid":
			_, _ = w.Write([]byte(`{"txID":"8888888888888888888888888888888888888888888888888888888888888888","raw_data":{"contract":[]}}`))
		case "/wallet/gettransactioninfobyid":
			if broadcastCalls < 2 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"8888888888888888888888888888888888888888888888888888888888888888","blockNumber":321,"receipt":{"net_usage":277}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	txid, err := client.BroadcastTx([]byte(`{"txID":"8888888888888888888888888888888888888888888888888888888888888888","raw_data":{"contract":[]}}`))
	if err != nil {
		t.Fatalf("expected TRON broadcast to succeed after rebroadcast, got %v", err)
	}
	if txid != "8888888888888888888888888888888888888888888888888888888888888888" {
		t.Fatalf("expected txid to be returned, got %s", txid)
	}
	if broadcastCalls < 2 {
		t.Fatalf("expected tx to be rebroadcast while waiting for confirmation, got %d broadcasts", broadcastCalls)
	}
}

func TestBroadcastTxFailsWhenTransactionNeverConfirms(t *testing.T) {
	restore := overrideBroadcastObservationTimings(25*time.Millisecond, time.Millisecond)
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/wallet/broadcasttransaction":
			_, _ = w.Write([]byte(`{"result":true,"txid":"9999999999999999999999999999999999999999999999999999999999999999"}`))
		case "/wallet/gettransactionbyid":
			_, _ = w.Write([]byte(`{"txID":"9999999999999999999999999999999999999999999999999999999999999999","raw_data":{"contract":[]}}`))
		case "/wallet/gettransactioninfobyid":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.BroadcastTx([]byte(`{"txID":"9999999999999999999999999999999999999999999999999999999999999999","raw_data":{"contract":[]}}`))
	if err == nil {
		t.Fatal("expected TRON broadcast to fail when the tx never confirms")
	}
	if !strings.Contains(err.Error(), "not confirmed on tron node") {
		t.Fatalf("expected missing-confirmation error, got %v", err)
	}
}

func overrideBroadcastObservationTimings(timeout, interval time.Duration) func() {
	prevTimeout := tronBroadcastObservationTimeout
	prevInterval := tronBroadcastObservationPollInterval
	prevRebroadcastInterval := tronBroadcastRebroadcastInterval
	tronBroadcastObservationTimeout = timeout
	tronBroadcastObservationPollInterval = interval
	tronBroadcastRebroadcastInterval = interval
	return func() {
		tronBroadcastObservationTimeout = prevTimeout
		tronBroadcastObservationPollInterval = prevInterval
		tronBroadcastRebroadcastInterval = prevRebroadcastInterval
	}
}

func newTestClient(t *testing.T, host string) *Client {
	t.Helper()

	keys := newTestKeys(t, "user"+strconv.FormatInt(time.Now().UnixNano(), 10), tronTestMnemonic)
	client, err := NewClient(common.TRONChain, host, keys)
	if err != nil {
		t.Fatalf("failed to create TRON simulation client: %v", err)
	}

	return client.(*Client)
}

func newTestSimTx(to common.Address) SimTx {
	return SimTx{
		Chain:     common.TRONChain,
		ToAddress: to,
		Coin:      common.NewCoin(common.TRXAsset, math.NewUint(100*common.One)),
	}
}

func newTestKeys(t *testing.T, name, mnemonic string) *thorclient.Keys {
	t.Helper()

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	kr := keyring.NewInMemory(cdc, func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{hd.Secp256k1}
	})

	if _, err := kr.NewAccount(name, mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatalf("failed to add account to keyring: %v", err)
	}

	return thorclient.NewKeysWithKeybase(kr, name, "")
}

func TestSignAndBroadcastRetainsOriginalTronSubmission(t *testing.T) {
	for _, scenario := range []string{
		"lost reply", "malformed reply", "empty reply", "application error", "unknown code",
		"other error", "duplicate", "accepted then disconnected", "different response ID", "different rejection ID",
		"lost reply then busy", "lost reply then validation error", "wrong receipt",
		"receipt lookup error", "expired original", "pending original",
	} {
		t.Run(scenario, func(t *testing.T) {
			restore := overrideBroadcastObservationTimings(100*time.Millisecond, time.Millisecond)
			defer restore()
			var creates, broadcasts atomic.Int32
			var mu sync.Mutex
			var payloads []string
			unresolved := scenario == "lost reply then busy" || scenario == "lost reply then validation error" ||
				scenario == "wrong receipt" || scenario == "receipt lookup error" || scenario == "expired original" || scenario == "pending original"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/wallet/createtransaction":
					n := creates.Add(1)
					expiry := int64(0)
					if scenario == "expired original" {
						expiry = time.Now().Add(-time.Minute).UnixMilli()
					}
					_, _ = fmt.Fprintf(w, `{"txID":"%064x","raw_data":{"contract":[],"expiration":%d}}`, n, expiry)
				case "/wallet/broadcasttransaction":
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					payloads = append(payloads, string(body))
					mu.Unlock()
					n := broadcasts.Add(1)
					if n == 1 {
						switch scenario {
						case "lost reply", "lost reply then busy", "lost reply then validation error":
							conn, _, hijackErr := w.(http.Hijacker).Hijack()
							if hijackErr != nil {
								t.Error(hijackErr)
								return
							}
							_ = conn.Close()
							return
						case "malformed reply":
							_, _ = w.Write([]byte(`{`))
							return
						case "empty reply":
							_, _ = w.Write([]byte(`{}`))
							return
						case "application error":
							_, _ = w.Write([]byte(`{"Error":"unavailable"}`))
							return
						case "unknown code":
							_, _ = w.Write([]byte(`{"result":false,"code":"NEW_ERROR"}`))
							return
						case "other error":
							_, _ = w.Write([]byte(`{"result":false,"code":"OTHER_ERROR"}`))
							return
						case "duplicate":
							_, _ = w.Write([]byte(`{"result":false,"code":"DUP_TRANSACTION_ERROR"}`))
							return
						case "accepted then disconnected":
							_, _ = w.Write([]byte(`{"result":false,"code":"NOT_ENOUGH_EFFECTIVE_CONNECTION","message":"P2P broadcast failed."}`))
							return
						case "different rejection ID":
							_, _ = fmt.Fprintf(w, `{"result":false,"code":"SIGERROR","txid":"%064x"}`, 99)
							return
						case "different response ID":
							_, _ = fmt.Fprintf(w, `{"result":true,"txid":"%064x"}`, 99)
							return
						}
					}
					if scenario == "lost reply then busy" {
						_, _ = w.Write([]byte(`{"result":false,"code":"SERVER_BUSY"}`))
						return
					}
					if scenario == "lost reply then validation error" {
						_, _ = w.Write([]byte(`{"result":false,"code":"CONTRACT_VALIDATE_ERROR"}`))
						return
					}
					_, _ = fmt.Fprintf(w, `{"result":true,"txid":"%064x"}`, 1)
				case "/wallet/gettransactioninfobyid":
					var lookup struct {
						Value string `json:"value"`
					}
					if err := json.NewDecoder(req.Body).Decode(&lookup); err != nil {
						t.Error(err)
						return
					}
					if lookup.Value != fmt.Sprintf("%064x", 1) {
						t.Errorf("queried a different transaction: %s", lookup.Value)
					}
					if scenario == "wrong receipt" {
						_, _ = fmt.Fprintf(w, `{"id":"%064x","blockNumber":100}`, 99)
						return
					}
					if scenario == "receipt lookup error" {
						_, _ = w.Write([]byte(`{"Error":"lookup unavailable"}`))
						return
					}
					if !unresolved && broadcasts.Load() >= 2 {
						_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":100}`, lookup.Value)
					} else {
						_, _ = w.Write([]byte(`{}`))
					}
				case "/wallet/gettransactionbyid":
					if scenario == "pending original" {
						_, _ = fmt.Fprintf(w, `{"txID":"%064x"}`, 1)
					} else {
						_, _ = w.Write([]byte(`{}`))
					}
				default:
					t.Errorf("unexpected request %s", req.URL.Path)
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			client := newTestClient(t, server.URL)
			txID, err := SignAndBroadcastTx(client, newTestSimTx(client.address))
			if unresolved {
				if !errors.Is(err, ErrSimTxUncertain) {
					t.Fatalf("unresolved submission must stop: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if creates.Load() != 1 || txID != fmt.Sprintf("%064x", 1) {
				t.Fatalf("lost original identity: signs=%d tx=%s err=%v", creates.Load(), txID, err)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, payload := range payloads {
				if payload != payloads[0] {
					t.Fatal("rebroadcast replaced the signed payload")
				}
			}
			if scenario == "expired original" {
				if broadcasts.Load() != 1 {
					t.Fatal("rebroadcast an expired transaction")
				}
			} else if broadcasts.Load() < 2 {
				t.Fatal("did not exercise same-payload rebroadcast")
			}
		})
	}
}

func TestSignAndBroadcastCanRetryDefiniteFirstTronRejection(t *testing.T) {
	restore := overrideBroadcastObservationTimings(100*time.Millisecond, time.Millisecond)
	defer restore()
	var creates, broadcasts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/wallet/createtransaction":
			_, _ = fmt.Fprintf(w, `{"txID":"%064x","raw_data":{"contract":[]}}`, creates.Add(1))
		case "/wallet/broadcasttransaction":
			if broadcasts.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"result":false,"code":"SERVER_BUSY"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"result":true,"txid":"%064x"}`, 2)
		case "/wallet/gettransactioninfobyid":
			var lookup struct {
				Value string `json:"value"`
			}
			if err := json.NewDecoder(req.Body).Decode(&lookup); err != nil {
				t.Error(err)
				return
			}
			if lookup.Value != fmt.Sprintf("%064x", 2) {
				t.Errorf("polled rejected transaction: %s", lookup.Value)
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"blockNumber":100}`, lookup.Value)
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	id, err := SignAndBroadcastTx(client, newTestSimTx(client.address))
	if err == nil || errors.Is(err, ErrSimTxUncertain) || id != "" || creates.Load() != 1 || broadcasts.Load() != 1 {
		t.Fatalf("first rejection must return without uncertainty: signs=%d broadcasts=%d id=%s err=%v", creates.Load(), broadcasts.Load(), id, err)
	}
	id, err = SignAndBroadcastTx(client, newTestSimTx(client.address))
	if err != nil || id != fmt.Sprintf("%064x", 2) || creates.Load() != 2 || broadcasts.Load() != 2 {
		t.Fatalf("definitely rejected transfer could not retry: signs=%d broadcasts=%d id=%s err=%v", creates.Load(), broadcasts.Load(), id, err)
	}
}
