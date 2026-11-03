package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/config"
)

// MetricName
type MetricName string

const (
	TotalBlockScanned       MetricName = `total_block_scanned`
	CurrentPosition         MetricName = `current_position`
	TotalRetryBlocks        MetricName = `total_retry_blocks`
	CommonBlockScannerError MetricName = `block_scanner_error`

	ThorchainBlockScannerError MetricName = `thorchain_block_scan_error`
	BlockDiscoveryDuration     MetricName = `block_discovery_duration`

	ThorchainClientError    MetricName = `thorchain_client_error`
	TxToThorchain           MetricName = `tx_to_thorchain`
	TxToThorchainSigned     MetricName = `tx_to_thorchain_signed`
	SignToThorchainDuration MetricName = `sign_to_thorchain_duration`
	SendToThorchainDuration MetricName = `send_to_thorchain_duration`

	XMRPendingCertificates         MetricName = `xmr_pending_certificates`
	XMROldestPendingCertificateAge MetricName = `xmr_oldest_pending_certificate_age`

	ObserverError MetricName = `observer_error`
	SignerError   MetricName = `signer_error`

	PubKeyManagerError      MetricName = `pubkey_manager_error`
	DashInstantSendObserved MetricName = `dash_instantsend_observed`
	InstantSendDeferred     MetricName = `dash_instantsend_deferred`
	InstantSendReserved     MetricName = `dash_instantsend_reserved`
	InstantSendAvailable    MetricName = `dash_instantsend_available`
	InstantSendPending      MetricName = `dash_instantsend_pending`
	InstantSendWarmingUp    MetricName = `dash_instantsend_warming_up`

	BatchSends      MetricName = `batch_sends`
	BatchClears     MetricName = `batch_clears`
	MessagesBatched MetricName = `messages_batched`
	BatchSize       MetricName = `batch_size`
	BatchSendTime   MetricName = `batch_send_time`

	// SolvencyPayloads counts the distinct exact-balance payloads observed for
	// TRON solvency rounds. Together with SolvencyRoundsStarted it exposes the
	// balance-fragmentation rate described in #3098.
	SolvencyPayloads MetricName = `solvency_payloads`
	// SolvencyRoundsStarted counts logical TRON rounds keyed internally by
	// chain, vault, and canonical report height.
	SolvencyRoundsStarted MetricName = `solvency_rounds_started`
	// SolvencyRounds counts TRON rounds that reached exact-payload quorum or
	// expired without quorum.
	SolvencyRounds MetricName = `solvency_rounds`
	// SolvencyRoundAttestations records the cumulative largest exact-payload
	// group and required quorum size for completed TRON rounds.
	SolvencyRoundAttestations MetricName = `solvency_round_attestations`

	// SolKeysignAbstain counts SOL keysign abstentions by reason. A fleet-wide spike
	// (every node failing IsBlockhashValid, or finalization lag exceeding the margin)
	// is otherwise invisible — the signer logs an abstain like routine non-selection.
	// A sustained anchor_expired_dropped rate means outbounds keep outliving Solana's
	// MAX_PROCESSING_AGE before they can be signed — most often because the RPCs no
	// longer retain finalized blocks back to the keysign height's target.
	SolKeysignAbstain MetricName = `sol_keysign_abstain`

	// UTXOKeysignPartyNotFormed counts UTXO outbounds whose unsigned tx was
	// discarded because the keysign party never formed. A sustained rate means
	// vault members keep building different txs (their bitcoind views of the
	// vault UTXOs differ) and the vault is not signing outbounds.
	UTXOKeysignPartyNotFormed MetricName = `utxo_keysign_party_not_formed`
)

// Metrics used to provide promethus metrics
type Metrics struct {
	logger zerolog.Logger
	cfg    config.BifrostMetricsConfiguration
	s      *http.Server
}

var (
	counters = map[MetricName]prometheus.Counter{
		TotalBlockScanned: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "block_scanner",
			Subsystem: "common_block_scanner",
			Name:      "total_block_scanned_total",
			Help:      "Total number of block scanned",
		}),
		CurrentPosition: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "block_scanner",
			Subsystem: "common_block_scanner",
			Name:      "current_position_total",
			Help:      "current block scan position",
		}),
		TotalRetryBlocks: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "block_scanner",
			Subsystem: "common_block_scanner",
			Name:      "total_retry_blocks_total",
			Help:      "total blocks retried ",
		}),
		TxToThorchain: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "thorchain_client",
			Name:      "tx_to_thorchain_total",
			Help:      "number of tx observer post to thorchain successfully",
		}),
		TxToThorchainSigned: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "thorchain_client",
			Name:      "tx_to_thorchain_signed_total",
			Help:      "number of tx observer signed successfully",
		}),
		DashInstantSendObserved: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "dash",
			Subsystem: "instantsend",
			Name:      "observed_total",
			Help:      "number of Dash InstantSend observations accepted from mempool",
		}),
		BatchSends: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_batcher",
			Name:      "batch_sends_total",
			Help:      "number of batch sends",
		}),
		BatchClears: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_batcher",
			Name:      "batch_clears_total",
			Help:      "number of batch clears",
		}),
	}
	counterVecs = map[MetricName]*prometheus.CounterVec{
		InstantSendDeferred: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "dash", Subsystem: "instantsend", Name: "deferred_total",
			Help: "InstantSend admission attempts deferred, by reason; repeated attempts are counted.",
		}, []string{"reason"}),
		CommonBlockScannerError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "block_scanner",
			Subsystem: "common_block_scanner",
			Name:      "errors_total",
			Help:      "errors in common block scanner",
		}, []string{
			"error_name", "additional",
		}),

		ThorchainBlockScannerError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "block_scanner",
			Subsystem: "thorchain_block_scanner",
			Name:      "errors_total",
			Help:      "errors in thorchain block scanner",
		}, []string{
			"error_name", "additional",
		}),

		ThorchainClientError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "thorchain",
			Subsystem: "thorchain_client",
			Name:      "errors_total",
			Help:      "errors in thorchain client",
		}, []string{
			"error_name", "additional",
		}),

		ObserverError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "observer",
			Name:      "errors_total",
			Help:      "errors in observer",
		}, []string{
			"error_name", "additional",
		}),
		SignerError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "signer",
			Subsystem: "signer",
			Name:      "errors_total",
			Help:      "errors in signer",
		}, []string{
			"error_name", "additional",
		}),
		SolKeysignAbstain: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "bifrost",
			Subsystem: "solana",
			Name:      "keysign_abstain_total",
			Help:      "SOL keysign abstentions by reason",
		}, []string{
			"reason",
		}),
		UTXOKeysignPartyNotFormed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "bifrost",
			Subsystem: "utxo",
			Name:      "keysign_party_not_formed_total",
			Help:      "UTXO unsigned txs discarded because the keysign party never formed",
		}, []string{
			"chain",
		}),
		PubKeyManagerError: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "pubkey_manager",
			Subsystem: "pubkey_manager",
			Name:      "errors_total",
			Help:      "errors in pubkey manager",
		}, []string{
			"error_name", "additional",
		}),
		MessagesBatched: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_batcher",
			Name:      "messages_batched_total",
			Help:      "number of messages batched",
		}, []string{
			"message_type",
		}),
		SolvencyPayloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_gossip",
			Name:      "solvency_payloads_total",
			Help:      "number of distinct exact TRON solvency payload variants observed",
		}, []string{
			"chain",
		}),
		SolvencyRoundsStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_gossip",
			Name:      "solvency_rounds_started_total",
			Help:      "number of logical TRON solvency rounds observed, grouped by chain, vault, and report height internally",
		}, []string{
			"chain",
		}),
		SolvencyRounds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_gossip",
			Name:      "solvency_rounds_total",
			Help:      "number of TRON solvency rounds that reached exact-payload quorum or expired without it",
		}, []string{
			"chain", "outcome",
		}),
		SolvencyRoundAttestations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "observer",
			Subsystem: "attestation_gossip",
			Name:      "solvency_round_attestations_total",
			Help:      "sum of largest exact-payload attestation groups and required quorum sizes when TRON solvency rounds reach quorum or expire",
		}, []string{
			"chain", "outcome", "measure",
		}),
	}

	histograms = map[MetricName]prometheus.Histogram{
		BlockDiscoveryDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "block_scanner",
			Subsystem: "common_block_scanner",
			Name:      "block_discovery",
			Help:      "how long it takes to discovery a block height",
		}),
		SignToThorchainDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "observer",
			Subsystem: "thorchain",
			Name:      "sign_to_thorchain_duration",
			Help:      "how long it takes to sign a tx to thorchain",
		}),
		SendToThorchainDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "observer",
			Subsystem: "thorchain",
			Name:      "send_to_thorchain_duration",
			Help:      "how long it takes to sign and broadcast to thorchain",
		}),
		BatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "observer",
			Subsystem: "attestation_batcher",
			Name:      "batch_size",
			Help:      "size of the batch",
		}),
		BatchSendTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "observer",
			Subsystem: "attestation_batcher",
			Name:      "batch_send_time",
			Help:      "how long it takes to send a batch",
		}),
	}

	gauges = map[MetricName]prometheus.Gauge{
		InstantSendReserved: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "dash", Subsystem: "instantsend", Name: "reserved_duffs",
			Help: "Customer InstantSend amount awaiting ChainLock settlement, in duffs.",
		}),
		InstantSendAvailable: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "dash", Subsystem: "instantsend", Name: "available_duffs",
			Help: "Available customer InstantSend allowance at the last mempool scan, in duffs.",
		}),
		InstantSendPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "dash", Subsystem: "instantsend", Name: "pending_transactions",
			Help: "Customer InstantSend reservations awaiting ChainLock settlement.",
		}),
		InstantSendWarmingUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "dash", Subsystem: "instantsend", Name: "warming_up",
			Help: "One while the startup InstantSend snapshot has not completed, zero once ready.",
		}),
		XMRPendingCertificates: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "observer", Subsystem: "xmr_certificate_outbox", Name: "pending",
			Help: "Pending certificates at the last successful bounded reconciliation scan, including retries in backoff.",
		}),
		XMROldestPendingCertificateAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "observer", Subsystem: "xmr_certificate_outbox", Name: "oldest_pending_age_seconds",
			Help: "Oldest pending certificate age at the last successful reconciliation scan; 0 if empty, -1 if any enqueue time is unknown.",
		}),
	}
)

// NewMetrics create a new instance of Metrics
func NewMetrics(cfg config.BifrostMetricsConfiguration) (*Metrics, error) {
	// Add chain metrics
	for _, chain := range cfg.Chains {
		AddChainMetrics(chain, counters, counterVecs, gauges, histograms)
	}
	// Register metrics
	for _, item := range counterVecs {
		prometheus.MustRegister(item)
	}
	for _, item := range counters {
		prometheus.MustRegister(item)
	}
	for _, item := range histograms {
		prometheus.MustRegister(item)
	}
	for _, item := range gauges {
		prometheus.MustRegister(item)
	}
	// create a new mux server
	server := http.NewServeMux()
	// register a new handler for the /metrics endpoint
	server.Handle(
		"/metrics",
		promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer,
			promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
				Timeout: cfg.WriteTimeout,
			}),
		),
	)

	// register pprof handlers if enabled
	if cfg.PprofEnabled {
		server.HandleFunc("/debug/pprof/", pprof.Index)
		server.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		server.HandleFunc("/debug/pprof/profile", pprof.Profile)
		server.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		server.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	// start an http server using the mux server
	s := &http.Server{
		Addr:        fmt.Sprintf(":%d", cfg.ListenPort),
		Handler:     server,
		ReadTimeout: cfg.ReadTimeout,
	}
	return &Metrics{
		logger: log.With().Str("module", "metrics").Logger(),
		cfg:    cfg,
		s:      s,
	}, nil
}

// GetCounter return a counter by name, if it doesn't exist, then it return nil
func (m *Metrics) GetCounter(name MetricName) prometheus.Counter {
	if counter, ok := counters[name]; ok {
		return counter
	}
	return nil
}

// GetHistograms return a histogram by name
func (m *Metrics) GetHistograms(name MetricName) prometheus.Histogram {
	if h, ok := histograms[name]; ok {
		return h
	}
	return nil
}

// GetGauges return a gauge by name
func (m *Metrics) GetGauge(name MetricName) prometheus.Gauge {
	if g, ok := gauges[name]; ok {
		return g
	}
	return nil
}

func (m *Metrics) GetCounterVec(name MetricName) *prometheus.CounterVec {
	if c, ok := counterVecs[name]; ok {
		return c
	}
	return nil
}

// Start
func (m *Metrics) Start() error {
	if !m.cfg.Enabled {
		return nil
	}
	go func() {
		m.logger.Info().Int("port", m.cfg.ListenPort).Msg("start metric server")
		if err := m.s.ListenAndServe(); err != nil {
			m.logger.Error().Err(err).Msg("fail to stop metric server")
		}
	}()
	return nil
}

// Stop
func (m *Metrics) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()
	return m.s.Shutdown(ctx)
}
