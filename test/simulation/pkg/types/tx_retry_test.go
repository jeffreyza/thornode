package types

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type TxRetrySuite struct{}

var _ = Suite(&TxRetrySuite{})

type retryLiteChainClient struct {
	signCalls         int
	broadcastCalls    int
	signErrs          []error
	broadcastErrs     []error
	broadcastTxIDs    []string
	broadcastPayloads [][]byte
}

func (r *retryLiteChainClient) GetAccount(_ *common.PubKey) (*common.Account, error) {
	return &common.Account{}, nil
}

func (r *retryLiteChainClient) SignTx(SimTx) ([]byte, error) {
	r.signCalls++
	idx := r.signCalls - 1
	if idx < len(r.signErrs) && r.signErrs[idx] != nil {
		return nil, r.signErrs[idx]
	}

	return []byte(fmt.Sprintf("signed-%d", r.signCalls)), nil
}

func (r *retryLiteChainClient) BroadcastTx(payload []byte) (string, error) {
	r.broadcastCalls++
	r.broadcastPayloads = append(r.broadcastPayloads, append([]byte(nil), payload...))
	idx := r.broadcastCalls - 1
	if idx < len(r.broadcastErrs) && r.broadcastErrs[idx] != nil {
		if idx < len(r.broadcastTxIDs) {
			return r.broadcastTxIDs[idx], r.broadcastErrs[idx]
		}
		return "", r.broadcastErrs[idx]
	}

	if idx < len(r.broadcastTxIDs) && r.broadcastTxIDs[idx] != "" {
		return r.broadcastTxIDs[idx], nil
	}
	return "ok", nil
}

func TestSignAndBroadcastTxReusesTronPayloadOnUncertainFailures(t *testing.T) {
	prevDelay := tronSimTxBroadcastRetryDelay
	prevAttempts := tronSimTxMaxBroadcastAttempts
	tronSimTxBroadcastRetryDelay = 0
	tronSimTxMaxBroadcastAttempts = 4
	defer func() {
		tronSimTxBroadcastRetryDelay = prevDelay
		tronSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastTxIDs: []string{"", "", "", "confirmed"},
		broadcastErrs: []error{
			errors.New("unexpected EOF"),
			errors.New("broadcasted tx unmined not confirmed on tron node"),
			errors.New("broadcasted tx invisible not observed on tron node"),
			nil,
		},
	}

	txID, err := SignAndBroadcastTx(client, SimTx{Chain: common.TRONChain})
	if err != nil {
		t.Fatalf("expected tron retry helper to recover, got %v", err)
	}
	if txID != "confirmed" {
		t.Fatalf("expected successful txid, got %s", txID)
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	for _, payload := range client.broadcastPayloads {
		if !bytes.Equal(payload, client.broadcastPayloads[0]) {
			t.Fatal("uncertain TRON retry replaced signed payload")
		}
	}
	if client.broadcastCalls != 4 {
		t.Fatalf("expected 4 broadcast attempts, got %d", client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxDoesNotReplaceUnconfirmedTron(t *testing.T) {
	client := &retryLiteChainClient{
		broadcastTxIDs: []string{"invisible", "duplicate"},
		broadcastErrs:  []error{errors.New("broadcasted tx invisible not observed on tron node"), nil},
	}
	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.TRONChain})
	if !errors.Is(err, ErrSimTxUncertain) || txid != "invisible" || client.signCalls != 1 || client.broadcastCalls != 1 {
		t.Fatalf("uncertain TRON transaction must retain its identity: id=%s err=%v signs=%d broadcasts=%d", txid, err, client.signCalls, client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxFailsUncertainOneShotMemo(t *testing.T) {
	prevDelay := tronSimTxBroadcastRetryDelay
	prevAttempts := tronSimTxMaxBroadcastAttempts
	tronSimTxBroadcastRetryDelay = 0
	tronSimTxMaxBroadcastAttempts = 4
	defer func() {
		tronSimTxBroadcastRetryDelay = prevDelay
		tronSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastTxIDs: []string{"unmined", "confirmed"},
		broadcastErrs: []error{
			errors.New("broadcasted tx unmined not confirmed on tron node"),
			nil,
		},
	}

	txID, err := SignAndBroadcastTx(client, SimTx{
		Chain:       common.TRONChain,
		Memo:        "r:00001",
		OneShotMemo: true,
	})
	if !errors.Is(err, ErrSimTxUncertain) {
		t.Fatalf("expected uncertain one-shot transfer to stop with an error, got %v", err)
	}
	if txID != "unmined" {
		t.Fatalf("expected original txid, got %s", txID)
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	if client.broadcastCalls != 1 {
		t.Fatalf("expected 1 broadcast attempt, got %d", client.broadcastCalls)
	}
}

// TestSignAndBroadcastTxAcceptsSubmittedSolanaTransaction verifies Solana
// submitted-but-unconfirmed broadcasts are treated as accepted one-shot results.
func (s *TxRetrySuite) TestSignAndBroadcastTxAcceptsSubmittedSolanaTransaction(c *C) {
	prevDelay := solanaSimTxBroadcastRetryDelay
	prevAttempts := solanaSimTxMaxBroadcastAttempts
	solanaSimTxBroadcastRetryDelay = 0
	solanaSimTxMaxBroadcastAttempts = 4
	defer func() {
		solanaSimTxBroadcastRetryDelay = prevDelay
		solanaSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastTxIDs: []string{"submitted"},
		broadcastErrs: []error{
			errors.New("broadcasted tx submitted not confirmed on solana node after 1m0s"),
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.SOLChain})
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "submitted")
	c.Assert(client.signCalls, Equals, 1)
	c.Assert(client.broadcastCalls, Equals, 1)
}

func TestSignAndBroadcastTxRetriesTronTransportFailures(t *testing.T) {
	prevDelay := tronSimTxBroadcastRetryDelay
	prevAttempts := tronSimTxMaxBroadcastAttempts
	tronSimTxBroadcastRetryDelay = 0
	tronSimTxMaxBroadcastAttempts = 4
	defer func() {
		tronSimTxBroadcastRetryDelay = prevDelay
		tronSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New(`failed to call broadcasttransaction: failed to perform request: Post "http://127.0.0.1:8090/wallet/broadcasttransaction": context deadline exceeded`),
			nil,
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.TRONChain})
	if err != nil {
		t.Fatalf("expected tron retry helper to recover from transport failure, got %v", err)
	}
	if txid != "ok" {
		t.Fatalf("expected successful txid, got %s", txid)
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	if !bytes.Equal(client.broadcastPayloads[0], client.broadcastPayloads[1]) {
		t.Fatal("transport retry changed the signed TRON payload")
	}
	if client.broadcastCalls != 2 {
		t.Fatalf("expected 2 broadcast attempts, got %d", client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxDoesNotRetryNonTronChains(t *testing.T) {
	prevDelay := tronSimTxBroadcastRetryDelay
	prevAttempts := tronSimTxMaxBroadcastAttempts
	tronSimTxBroadcastRetryDelay = 0
	tronSimTxMaxBroadcastAttempts = 4
	defer func() {
		tronSimTxBroadcastRetryDelay = prevDelay
		tronSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New("broadcasted tx a not confirmed on tron node"),
		},
	}

	_, err := SignAndBroadcastTx(client, SimTx{Chain: common.BTCChain})
	if err == nil {
		t.Fatal("expected non-tron tx to fail without retry")
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	if client.broadcastCalls != 1 {
		t.Fatalf("expected 1 broadcast attempt, got %d", client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxRetriesSolanaConfirmationFailures(t *testing.T) {
	prevDelay := solanaSimTxBroadcastRetryDelay
	prevAttempts := solanaSimTxMaxBroadcastAttempts
	solanaSimTxBroadcastRetryDelay = 0
	solanaSimTxMaxBroadcastAttempts = 4
	defer func() {
		solanaSimTxBroadcastRetryDelay = prevDelay
		solanaSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New("broadcasted tx a not confirmed on solana node after 12s"),
			errors.New("failed to broadcast tx: dial tcp 127.0.0.1:8899: connect: connection refused"),
			nil,
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.SOLChain})
	if err != nil {
		t.Fatalf("expected solana retry helper to recover, got %v", err)
	}
	if txid != "ok" {
		t.Fatalf("expected successful txid, got %s", txid)
	}
	if client.signCalls != 3 {
		t.Fatalf("expected 3 sign attempts, got %d", client.signCalls)
	}
	if client.broadcastCalls != 3 {
		t.Fatalf("expected 3 broadcast attempts, got %d", client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxRetriesSolanaBlockhashFailures(t *testing.T) {
	prevDelay := solanaSimTxBroadcastRetryDelay
	prevAttempts := solanaSimTxMaxBroadcastAttempts
	solanaSimTxBroadcastRetryDelay = 0
	solanaSimTxMaxBroadcastAttempts = 4
	defer func() {
		solanaSimTxBroadcastRetryDelay = prevDelay
		solanaSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		signErrs: []error{
			errors.New("fail to get latest blockhash: Post http://127.0.0.1:8899: dial tcp 127.0.0.1:8899: connect: connection refused"),
			nil,
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.SOLChain})
	if err != nil {
		t.Fatalf("expected solana retry helper to recover from blockhash failure, got %v", err)
	}
	if txid != "ok" {
		t.Fatalf("expected successful txid, got %s", txid)
	}
	if client.signCalls != 2 {
		t.Fatalf("expected 2 sign attempts, got %d", client.signCalls)
	}
	if client.broadcastCalls != 1 {
		t.Fatalf("expected 1 broadcast attempt, got %d", client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxRetriesZecQueuedDownloadFailures(t *testing.T) {
	prevDelay := zecSimTxBroadcastRetryDelay
	prevAttempts := zecSimTxMaxBroadcastAttempts
	zecSimTxBroadcastRetryDelay = 0
	zecSimTxMaxBroadcastAttempts = 4
	defer func() {
		zecSimTxBroadcastRetryDelay = prevDelay
		zecSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New("transaction dropped because it is already queued for download"),
			nil,
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.ZECChain})
	if err != nil {
		t.Fatalf("expected zec retry helper to recover from queued-download failure, got %v", err)
	}
	if txid != "ok" {
		t.Fatalf("expected successful txid, got %s", txid)
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	if client.broadcastCalls != 2 {
		t.Fatalf("expected 2 broadcast attempts, got %d", client.broadcastCalls)
	}
}

// TestSignAndBroadcastTxRetriesZecChannelClosedFailures covers the transient
// Zebra RPC channel close seen during mocknet ZEC account seeding.
func TestSignAndBroadcastTxRetriesZecChannelClosedFailures(t *testing.T) {
	prevDelay := zecSimTxBroadcastRetryDelay
	prevAttempts := zecSimTxMaxBroadcastAttempts
	zecSimTxBroadcastRetryDelay = 0
	zecSimTxMaxBroadcastAttempts = 4
	defer func() {
		zecSimTxBroadcastRetryDelay = prevDelay
		zecSimTxMaxBroadcastAttempts = prevAttempts
	}()

	// A closed RPC channel does not prove the funding transaction is invalid,
	// so the simulation should rebroadcast the original bytes instead of exiting.
	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New("channel closed"),
			nil,
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.ZECChain})
	if err != nil {
		t.Fatalf("expected zec retry helper to recover from channel-closed failure, got %v", err)
	}
	if txid != "ok" {
		t.Fatalf("expected successful txid, got %s", txid)
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	if client.broadcastCalls != 2 {
		t.Fatalf("expected 2 broadcast attempts, got %d", client.broadcastCalls)
	}
}

func TestShouldRetryTronSimTxBroadcast(t *testing.T) {
	if !shouldRetryTronSimTxBroadcast(errors.New("broadcasted tx a not confirmed on tron node")) {
		t.Fatal("expected confirmation failure to be retriable")
	}
	if !shouldRetryTronSimTxBroadcast(errors.New("broadcasted tx a not observed on tron node")) {
		t.Fatal("expected observation failure to be retriable")
	}
	if !shouldRetryTronSimTxBroadcast(errors.New(`failed to call broadcasttransaction: failed to perform request: Post "http://127.0.0.1:8090/wallet/broadcasttransaction": context deadline exceeded`)) {
		t.Fatal("expected transient TRON transport failures to be retriable")
	}
	if shouldRetryTronSimTxBroadcast(errors.New("other error")) {
		t.Fatal("expected unrelated errors to be non-retriable")
	}
	if shouldRetryTronSimTxBroadcast(nil) {
		t.Fatal("expected nil error to be non-retriable")
	}
}

func TestShouldRetryZecSimTxBroadcast(t *testing.T) {
	if !shouldRetryZecSimTxBroadcast(errors.New("transaction dropped because it is already queued for download")) {
		t.Fatal("expected queued-download failure to be retriable")
	}
	if !shouldRetryZecSimTxBroadcast(errors.New("failed to broadcast tx: context deadline exceeded")) {
		t.Fatal("expected transient zec RPC failure to be retriable")
	}
	if !shouldRetryZecSimTxBroadcast(errors.New("channel closed")) {
		t.Fatal("expected closed zec RPC channel failure to be retriable")
	}
	if shouldRetryZecSimTxBroadcast(errors.New("other error")) {
		t.Fatal("expected unrelated errors to be non-retriable")
	}
	if shouldRetryZecSimTxBroadcast(nil) {
		t.Fatal("expected nil error to be non-retriable")
	}
}

func TestShouldRetrySolanaSimTxFailures(t *testing.T) {
	if !shouldRetrySolanaSimTxBroadcast(errors.New("broadcasted tx a not confirmed on solana node after 12s")) {
		t.Fatal("expected confirmation failure to be retriable")
	}
	if !shouldRetrySolanaSimTxBroadcast(errors.New("failed to broadcast tx: dial tcp 127.0.0.1:8899: connect: connection refused")) {
		t.Fatal("expected transient solana RPC failure to be retriable")
	}
	if !shouldRetrySolanaSimTxSign(errors.New("fail to get latest blockhash: Post http://127.0.0.1:8899: dial tcp 127.0.0.1:8899: connect: connection refused")) {
		t.Fatal("expected latest blockhash failure to be retriable")
	}
	if shouldRetrySolanaSimTxBroadcast(errors.New("other error")) {
		t.Fatal("expected unrelated broadcast errors to be non-retriable")
	}
	if shouldRetrySolanaSimTxSign(errors.New("other error")) {
		t.Fatal("expected unrelated sign errors to be non-retriable")
	}
}

func TestZecBroadcastRetryKeepsOriginalPayload(t *testing.T) {
	previous := zecSimTxBroadcastRetryDelay
	zecSimTxBroadcastRetryDelay = 0
	t.Cleanup(func() { zecSimTxBroadcastRetryDelay = previous })
	for _, tc := range []struct {
		name     string
		errs     []error
		attempts int
		success  bool
	}{
		{"cancelled then accepted", []error{errors.New("verification cancelled"), nil}, 2, true},
		{"verification timeout", []error{errors.New("timeout waiting for verification result"), nil}, 2, true},
		{"exhausted", []error{errors.New("verification cancelled"), errors.New("channel closed"), errors.New("verification cancelled"), errors.New("verification cancelled")}, 4, false},
		{"invalid", []error{errors.New("failed to validate tx: bad-txns-inputs-missingorspent")}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &retryLiteChainClient{broadcastErrs: tc.errs}
			txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.ZECChain})
			if (err == nil) != tc.success || (txid != "") != tc.success {
				t.Fatalf("unexpected outcome: txid=%q err=%v", txid, err)
			}
			if client.signCalls != 1 || client.broadcastCalls != tc.attempts {
				t.Fatalf("signed %d times, broadcast %d times", client.signCalls, client.broadcastCalls)
			}
			for _, payload := range client.broadcastPayloads {
				if !bytes.Equal(payload, []byte("signed-1")) {
					t.Fatalf("replacement payload: %q", payload)
				}
			}
		})
	}
}

func TestSignAndBroadcastTxPreservesTerminalUncertainty(t *testing.T) {
	client := &retryLiteChainClient{
		broadcastTxIDs: []string{"unresolved", "unsafe-replacement"},
		broadcastErrs:  []error{fmt.Errorf("%w: not observed on tron node", ErrSimTxUncertain), nil},
	}
	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.TRONChain})
	if !errors.Is(err, ErrSimTxUncertain) || txid != "unresolved" || client.signCalls != 1 || client.broadcastCalls != 1 {
		t.Fatalf("uncertain submission was retried or accepted: id=%s err=%v signs=%d broadcasts=%d", txid, err, client.signCalls, client.broadcastCalls)
	}
}

func TestSimTxErrorResultStopsUncertainActor(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%v", uncertain), func(t *testing.T) {
			a := NewActor("broadcast failure", testRNG())
			attempts := 0
			completed := false
			a.Ops = []Op{
				func(*OpConfig) OpResult {
					attempts++
					if attempts > 1 {
						return OpResult{Continue: true}
					}
					err := errors.New("temporary lookup failure")
					if uncertain {
						err = fmt.Errorf("%w: %w", ErrSimTxUncertain, err)
					}
					return SimTxErrorResult(err)
				},
				func(*OpConfig) OpResult { completed = true; return OpResult{Finish: true} },
			}
			err := a.Execute(nil)
			if uncertain {
				if !errors.Is(err, ErrSimTxUncertain) || attempts != 1 || completed {
					t.Fatalf("uncertain actor retried or completed: attempts=%d completed=%v err=%v", attempts, completed, err)
				}
			} else if err != nil || attempts != 2 || !completed {
				t.Fatalf("ordinary failure did not retry: attempts=%d completed=%v err=%v", attempts, completed, err)
			}
		})
	}
}
