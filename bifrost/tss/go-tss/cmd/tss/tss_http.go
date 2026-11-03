package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
)

// TssHttpServer provide http endpoint for tss server
type TssHttpServer struct {
	logger     zerolog.Logger
	tssServer  tss.Server
	s          *http.Server
	authSecret string
}

// tssHTTPMaxRequestBodyBytes is intentionally small: keygen/keysign requests
// carry pubkeys and a bounded batch of encoded signing messages, so 1 MiB is
// generous for normal operation while still capping trivial memory abuse.
const tssHTTPMaxRequestBodyBytes int64 = 1 << 20

// NewTssHttpServer creates the TSS HTTP server without optional bearer auth.
func NewTssHttpServer(tssAddr string, t tss.Server) *TssHttpServer {
	return NewTssHttpServerWithAuth(tssAddr, t, "")
}

// NewTssHttpServerWithAuth should only listen to the loopback.
func NewTssHttpServerWithAuth(tssAddr string, t tss.Server, authSecret string) *TssHttpServer {
	hs := &TssHttpServer{
		logger:     log.With().Str("module", "http").Logger(),
		tssServer:  t,
		authSecret: strings.TrimSpace(authSecret),
	}
	s := &http.Server{
		Addr:    tssAddr,
		Handler: hs.tssNewHandler(),
	}
	hs.s = s
	return hs
}

// NewHandler registers the API routes and returns a new HTTP handler
func (t *TssHttpServer) tssNewHandler() http.Handler {
	router := mux.NewRouter()
	// All non-metrics endpoints use optional bearer auth. If no secret is
	// configured, loopback-only mode preserves existing behavior. /metrics
	// stays unauthenticated because Start enforces loopback binding.
	router.Handle("/keygen", http.HandlerFunc(t.keygenHandler)).Methods(http.MethodPost)
	router.Handle("/keygenall", http.HandlerFunc(t.keygenAllHandler)).Methods(http.MethodPost)
	router.Handle("/keysign", http.HandlerFunc(t.keySignHandler)).Methods(http.MethodPost)
	router.Handle("/ping", http.HandlerFunc(t.pingHandler)).Methods(http.MethodGet)
	router.Handle("/p2pid", http.HandlerFunc(t.getP2pIDHandler)).Methods(http.MethodGet)
	router.Handle("/metrics", promhttp.Handler())
	router.Use(logMiddleware())
	return router
}

func (t *TssHttpServer) keygenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !t.ensureAuthorized(w, r) {
		return
	}
	defer func() {
		if err := r.Body.Close(); nil != err {
			t.logger.Error().Err(err).Msg("fail to close request body")
		}
	}()
	t.logger.Info().Msg("receive key gen request")
	var keygenReq keygen.Request
	if !t.decodeJSONRequest(w, r, &keygenReq, "keygen") {
		return
	}

	resp, err := t.tssServer.Keygen(keygenReq)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to key gen")
	}
	t.logger.Debug().Msgf("resp:%+v", resp)
	buf, err := json.Marshal(resp)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to marshal response to json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = w.Write(buf)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to write to response")
	}
}

func (t *TssHttpServer) keygenAllHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !t.ensureAuthorized(w, r) {
		return
	}
	defer func() {
		if err := r.Body.Close(); nil != err {
			t.logger.Error().Err(err).Msg("fail to close request body")
		}
	}()
	t.logger.Info().Msg("receive key gen request")
	var keygenReq keygen.Request
	if !t.decodeJSONRequest(w, r, &keygenReq, "keygenall") {
		return
	}

	resp, err := t.tssServer.KeygenAllAlgo(keygenReq)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to key gen")
	}
	t.logger.Debug().Msgf("resp:%+v", resp)
	buf, err := json.Marshal(resp)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to marshal response to json")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = w.Write(buf)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to write to response")
	}
}

func (t *TssHttpServer) keySignHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !t.ensureAuthorized(w, r) {
		return
	}
	defer func() {
		if err := r.Body.Close(); nil != err {
			t.logger.Error().Err(err).Msg("fail to close request body")
		}
	}()
	t.logger.Info().Msg("receive key sign request")

	var keySignReq keysign.Request
	if !t.decodeJSONRequest(w, r, &keySignReq, "keysign") {
		return
	}
	t.logger.Info().Msgf("request:%+v", keySignReq)
	signResp, err := t.tssServer.KeySign(keySignReq)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to key sign")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	jsonResult, err := json.MarshalIndent(signResp, "", "	")
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to marshal response to json message")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = w.Write(jsonResult)
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to write response")
	}
}

func (t *TssHttpServer) Start() error {
	if t.s == nil {
		return errors.New("invalid http server instance")
	}
	if err := validateLoopbackListenAddr(t.s.Addr); err != nil {
		return fmt.Errorf("refusing to start tss http server: %w", err)
	}
	if t.authSecret == "" {
		t.logger.Warn().Str("addr", t.s.Addr).Msg("tss http auth is disabled; relying on loopback bind only")
	}
	if err := t.tssServer.Start(); err != nil {
		return fmt.Errorf("fail to start tss server: %w", err)
	}
	if err := t.s.ListenAndServe(); err != nil {
		if err != http.ErrServerClosed {
			return fmt.Errorf("fail to start http server: %w", err)
		}
	}

	return nil
}

func logMiddleware() mux.MiddlewareFunc {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Debug().
				Str("route", r.URL.Path).
				Str("port", r.URL.Port()).
				Str("method", r.Method).
				Msg("HTTP request received")

			handler.ServeHTTP(w, r)
		})
	}
}

func validateLoopbackListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("invalid bind address %q: %w", addr, err)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("bind address %q must specify a loopback host", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("bind address host %q is not loopback", host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("failed to resolve bind address host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("bind address host %q did not resolve to any addresses", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("bind address host %q resolved to non-loopback address %s", host, ip)
		}
	}
	return nil
}

func (t *TssHttpServer) ensureAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if t.authSecret == "" {
		return true
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authHeader, "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if subtle.ConstantTimeCompare([]byte(token), []byte(t.authSecret)) == 1 {
		return true
	}
	w.WriteHeader(http.StatusUnauthorized)
	return false
}

func (t *TssHttpServer) decodeJSONRequest(w http.ResponseWriter, r *http.Request, out interface{}, name string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, tssHTTPMaxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(out); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			t.logger.Error().Err(err).Str("request", name).Msg("tss http request body too large")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return false
		}
		t.logger.Error().Err(err).Str("request", name).Msg("fail to decode tss http request")
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	return true
}

func (t *TssHttpServer) Stop() error {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := t.s.Shutdown(c)
	if err != nil {
		log.Error().Err(err).Msg("Failed to shutdown the Tss server gracefully")
	}
	t.tssServer.Stop()
	return err
}

func (t *TssHttpServer) pingHandler(w http.ResponseWriter, r *http.Request) {
	if !t.ensureAuthorized(w, r) {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (t *TssHttpServer) getP2pIDHandler(w http.ResponseWriter, r *http.Request) {
	if !t.ensureAuthorized(w, r) {
		return
	}
	localPeerID := t.tssServer.GetLocalPeerID()
	_, err := w.Write([]byte(localPeerID))
	if err != nil {
		t.logger.Error().Err(err).Msg("fail to write to response")
	}
}
