package sidecar

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Engine is the local FROST operations surface exposed by Server. Method
// names mirror Client so a sidecar.Client satisfies the interface directly.
type Engine interface {
	// DKG
	DKGRound1(req DKGRound1Req) (DKGRound1Resp, error)
	DKGRound2(req DKGRound2Req) (DKGRound2Resp, error)
	DKGRound3(req DKGRound3Req) (DKGRound3Resp, error)

	// Signing
	SignPrepare(req SignPrepareReq) (SignPrepareResp, error)
	SignPartial(req SignPartialReq) (SignPartialResp, error)
	SignCombine(req SignCombineReq) (SignCombineResp, error)

	// Broadcast
	Broadcast(req BroadcastReq) (BroadcastResp, error)
}

type healthChecker interface {
	HealthCheck(ctx context.Context) error
}

// Server exposes Engine over HTTP. It mirrors the sidecar's own wire contract
// so a peer-side engine could plug in behind the same routes.
type Server struct {
	logger   zerolog.Logger
	engine   Engine
	server   *http.Server
	username string
	password string
	bearer   string
	now      func() time.Time
}

const serverMaxRequestBodyBytes = 1 << 20 // 1 MiB

// NewServer constructs a Server with no authentication configured. It refuses
// to start on non-loopback addresses.
func NewServer(addr string, engine Engine, logger zerolog.Logger) *Server {
	return NewServerWithAuth(addr, engine, logger, "", "", "")
}

// NewServerWithAuth constructs a Server with optional basic/bearer auth.
func NewServerWithAuth(addr string, engine Engine, logger zerolog.Logger, username, password, bearer string) *Server {
	mux := http.NewServeMux()
	s := &Server{
		logger:   logger,
		engine:   engine,
		username: strings.TrimSpace(username),
		password: strings.TrimSpace(password),
		bearer:   strings.TrimSpace(bearer),
		now:      time.Now,
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
	mux.HandleFunc("/v1/monero/dkg/round1", s.handleDKGRound1)
	mux.HandleFunc("/v1/monero/dkg/round2", s.handleDKGRound2)
	mux.HandleFunc("/v1/monero/dkg/round3", s.handleDKGRound3)
	mux.HandleFunc("/v1/monero/sign/prepare", s.handleSignPrepare)
	mux.HandleFunc("/v1/monero/sign/partial", s.handleSignPartial)
	mux.HandleFunc("/v1/monero/sign/combine", s.handleSignCombine)
	mux.HandleFunc("/v1/monero/broadcast", s.handleBroadcast)
	mux.HandleFunc("/health", s.handleHealth)
	return s
}

func (s *Server) Start() error {
	if s.engine == nil {
		return fmt.Errorf("engine is nil")
	}
	if s.username == "" && s.bearer == "" && !isLoopbackListenAddr(s.server.Addr) {
		return fmt.Errorf("unauthenticated sidecar server must bind to loopback")
	}
	s.logger.Info().Str("addr", s.server.Addr).Msg("starting monero sidecar server")
	err := s.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) ensureAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if s.username == "" && s.bearer == "" {
		return true
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if s.bearer != "" && strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if secureEqual(token, s.bearer) {
			return true
		}
	}
	if s.username != "" {
		username, password, ok := r.BasicAuth()
		if ok && secureEqual(username, s.username) && secureEqual(password, s.password) {
			return true
		}
	}
	writeJSONError(w, http.StatusUnauthorized, "unauthorized")
	return false
}

func (s *Server) handleDKGRound1(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req DKGRound1Req
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.DKGRound1(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDKGRound2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req DKGRound2Req
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.DKGRound2(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDKGRound3(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req DKGRound3Req
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.DKGRound3(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSignPrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req SignPrepareReq
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.SignPrepare(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSignPartial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req SignPartialReq
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.SignPartial(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSignCombine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req SignCombineReq
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.SignCombine(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.ensureAuthorized(w, r) {
		return
	}
	var req BroadcastReq
	if err := decodeJSONRequest(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	resp, err := s.engine.Broadcast(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.engine == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "engine is not configured")
		return
	}
	if checker, ok := s.engine.(healthChecker); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := checker.HealthCheck(ctx); err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, fmt.Sprintf("engine is unhealthy: %v", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": message,
	})
}

func decodeJSONRequest(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, serverMaxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain a single json object")
		}
		return err
	}
	return nil
}

// IsAlreadyRelayedError reports whether err carries a daemon message that
// indicates the tx was already in the pool / already seen. Callers use this
// to treat the broadcast as idempotent.
func IsAlreadyRelayedError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "already in pool") ||
		strings.Contains(lower, "already spent") ||
		strings.Contains(lower, "tx already")
}

func isLoopbackListenAddr(addr string) bool {
	host := strings.TrimSpace(addr)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSpace(splitHost)
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
