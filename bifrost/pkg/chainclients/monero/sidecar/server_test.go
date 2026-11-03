package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

type mockEngine struct {
	healthErr error

	dkgRound1Resp   DKGRound1Resp
	dkgRound1Err    error
	dkgRound2Resp   DKGRound2Resp
	dkgRound2Err    error
	dkgRound3Resp   DKGRound3Resp
	dkgRound3Err    error
	signPrepareResp SignPrepareResp
	signPrepareErr  error
	signPartialResp SignPartialResp
	signPartialErr  error
	signCombineResp SignCombineResp
	signCombineErr  error
	broadcastResp   BroadcastResp
	broadcastErr    error
}

func (m *mockEngine) HealthCheck(_ context.Context) error { return m.healthErr }

func (m *mockEngine) DKGRound1(_ DKGRound1Req) (DKGRound1Resp, error) {
	return m.dkgRound1Resp, m.dkgRound1Err
}

func (m *mockEngine) DKGRound2(_ DKGRound2Req) (DKGRound2Resp, error) {
	return m.dkgRound2Resp, m.dkgRound2Err
}

func (m *mockEngine) DKGRound3(_ DKGRound3Req) (DKGRound3Resp, error) {
	return m.dkgRound3Resp, m.dkgRound3Err
}

func (m *mockEngine) SignPrepare(_ SignPrepareReq) (SignPrepareResp, error) {
	return m.signPrepareResp, m.signPrepareErr
}

func (m *mockEngine) SignPartial(_ SignPartialReq) (SignPartialResp, error) {
	return m.signPartialResp, m.signPartialErr
}

func (m *mockEngine) SignCombine(_ SignCombineReq) (SignCombineResp, error) {
	return m.signCombineResp, m.signCombineErr
}

func (m *mockEngine) Broadcast(_ BroadcastReq) (BroadcastResp, error) {
	return m.broadcastResp, m.broadcastErr
}

func TestServerDKGRound1(t *testing.T) {
	t.Parallel()

	engine := &mockEngine{dkgRound1Resp: DKGRound1Resp{Commitments: "commit_hex"}}
	service := NewServerWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(DKGRound1Req{
		VaultPubKey:      "thorpub1vault",
		Threshold:        2,
		N:                3,
		ParticipantIndex: 1,
		SessionID:        "dkg-session",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/dkg/round1", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post dkg round1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestServerSignPrepare(t *testing.T) {
	t.Parallel()

	engine := &mockEngine{signPrepareResp: SignPrepareResp{SessionID: "session-1", Preprocess: "preprocess_hex"}}
	service := NewServerWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(SignPrepareReq{
		VaultPubKey:  "thorpub1vault",
		ToAddress:    "48abc",
		AmountAtomic: 100,
		DecoyContext: "decoy-context",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/sign/prepare", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post sign prepare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestServerBroadcast(t *testing.T) {
	t.Parallel()

	engine := &mockEngine{broadcastResp: BroadcastResp{TxHash: "abc123"}}
	service := NewServerWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(BroadcastReq{TxHex: "deadbeef", MsgID: "broadcast-msg-id"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/broadcast", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post broadcast: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestServerHealth(t *testing.T) {
	t.Parallel()

	service := NewServerWithAuth("127.0.0.1:0", &mockEngine{}, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestServerHealthUnhealthyEngine(t *testing.T) {
	t.Parallel()

	service := NewServerWithAuth("127.0.0.1:0", &mockEngine{healthErr: errors.New("signer unavailable")}, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", resp.StatusCode)
	}
}

func TestServerHealthNilEngine(t *testing.T) {
	t.Parallel()

	service := NewServerWithAuth("127.0.0.1:0", nil, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", resp.StatusCode)
	}
}

func TestIsAlreadyRelayedError(t *testing.T) {
	t.Parallel()

	if !IsAlreadyRelayedError(errors.New("tx already in pool")) {
		t.Fatalf("expected already-relayed error to be recognized")
	}
	if IsAlreadyRelayedError(errors.New("connection refused")) {
		t.Fatalf("expected non-relay error to be rejected")
	}
}

func TestServerRejectsUnauthorizedWhenAuthConfigured(t *testing.T) {
	t.Parallel()

	engine := &mockEngine{dkgRound1Resp: DKGRound1Resp{Commitments: "commit"}}
	service := NewServerWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "user", "pass", "token")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(DKGRound1Req{VaultPubKey: "thorpub1vault", SessionID: "s1", Threshold: 2, N: 3, ParticipantIndex: 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/dkg/round1", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.StatusCode)
	}
}

func TestServerAllowsAuthorizedBearer(t *testing.T) {
	t.Parallel()

	engine := &mockEngine{dkgRound1Resp: DKGRound1Resp{Commitments: "commit"}}
	service := NewServerWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "token")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(DKGRound1Req{VaultPubKey: "thorpub1vault", SessionID: "s1", Threshold: 2, N: 3, ParticipantIndex: 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/monero/dkg/round1", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestServerStartRejectsUnauthenticatedNonLoopback(t *testing.T) {
	t.Parallel()

	service := NewServerWithAuth("0.0.0.0:0", &mockEngine{}, zerolog.New(io.Discard), "", "", "")
	err := service.Start()
	if err == nil {
		t.Fatalf("expected start failure for unauthenticated non-loopback listen")
	}
}
