package config

import (
	"reflect"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

////////////////////////////////////////////////////////////////////////////////////////
// Constants
////////////////////////////////////////////////////////////////////////////////////////

const (
	EmojiMoneybag         = ":moneybag:"
	EmojiMoneyWithWings   = ":money_with_wings:"
	EmojiDollar           = ":dollar:"
	EmojiWhiteCheckMark   = ":white_check_mark:"
	EmojiSmallRedTriangle = ":small_red_triangle:"
	EmojiRotatingLight    = ":rotating_light:"
)

////////////////////////////////////////////////////////////////////////////////////////
// Webhooks
////////////////////////////////////////////////////////////////////////////////////////

type Webhooks struct {
	Category  string `mapstructure:"name"`
	Slack     string `mapstructure:"slack"`
	Discord   string `mapstructure:"discord"`
	PagerDuty string `mapstructure:"pagerduty"`
}

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

// Get returns the global configuration.
func Get() Config {
	return config
}

// Set sets the global configuration (for testing).
func Set(c Config) {
	config = c
}

// Config contains all configuration for the application.
type Config struct {
	// StoragePath is parent directory for any persisted state.
	StoragePath string `mapstructure:"storage_path"`

	// Console enables console mode with pretty output and notifications to terminal.
	Console bool `mapstructure:"console"`

	// MaxRetries is the number of times to retry requests with backoff.
	MaxRetries int `mapstructure:"max_retries"`

	// Network is the network to user for prefixes.
	Network string `mapstructure:"network"`

	// Scan contains overrides for the heights to scan.
	Scan struct {
		// Start is the block height to start scanning from.
		Start int `mapstructure:"start"`

		// Stop is the block height to stop scanning at.
		Stop int `mapstructure:"stop"`
	} `mapstructure:"scan"`

	// Endpoints contain URLs to services that are used in block scanning.
	Endpoints struct {
		// CacheSize is the number responses to keep in LRU cache.
		CacheSize int `mapstructure:"cache_size"`

		Thornode string `mapstructure:"thornode"`
		Midgard  string `mapstructure:"midgard"`
	} `mapstructure:"endpoints"`

	// Notifications contain categories of webhooks that route to multiple services.
	Notifications struct {
		Activity      Webhooks `mapstructure:"activity"`
		Lending       Webhooks `mapstructure:"lending"`
		Info          Webhooks `mapstructure:"info"`
		Security      Webhooks `mapstructure:"security"`
		Reschedules   Webhooks `mapstructure:"reschedules"`
		FailedRefunds Webhooks `mapstructure:"failed_refunds"`
	} `mapstructure:"notifications"`

	// Links contain URLs to services linked in alerts.
	Links struct {
		// Track is the THORChain Tracker service (https://gitlab.com/thorchain/track).
		Track string `mapstructure:"track"`

		// Explorer is the native Thorchain explorer, should support:
		// - <explorer>/tx/<txid>
		// - <explorer>/address/<address>
		// - <explorer>/block/<height>
		Explorer string `mapstructure:"explorer"`

		// Thornode is the Thornode API endpoint to use in message links.
		Thornode string `mapstructure:"thornode"`
	} `mapstructure:"explorers"`

	// TORAnchorCheckBlocks is the number of blocks to check for TOR anchor drift.
	TORAnchorCheckBlocks int64 `mapstructure:"tor_anchor_check_blocks"`

	// Thresholds contain various thresholds for alerts.
	Thresholds struct {
		// USDValue is the threshold for inbounds, outbounds, and swaps that trigger an
		// alert. The threshold is also increased by the clout value of corresponding
		// addresses to avoid noise from addresses with a history of trust.
		USDValue uint64 `mapstructure:"usd_value"`

		// RuneTransferValue is the threshold for a RUNE transfer that triggers an alert.
		RuneTransferValue uint64 `mapstructure:"rune_value"`

		// SwapDelta contains thresholds for USD value and percent change of a swap. The
		// alert will fire if both thresholds are met.
		SwapDelta struct {
			USDValue    uint64 `mapstructure:"usd_value"`
			BasisPoints int64  `mapstructure:"basis_points"`
		} `mapstructure:"swap_delta"`

		Security struct {
			USDValue uint64 `mapstructure:"usd_value"`
		} `mapstructure:"security"`

		// SwapSlipBasisPoints defines the slip threshold that will trigger a swap alert.
		SwapSlipBasisPoints uint64 `mapstructure:"swap_slip_basis_points"`

		// TORAnchorDriftBasisPoints defines the threshold for the drift of a single TOR
		// anchor asset that will trigger an alert.
		TORAnchorDriftBasisPoints uint64 `mapstructure:"tor_anchor_drift_basis_points"`

		// MaxRescheduledAge is the maximum age of a rescheduled outbound that will trigger
		// an alert. This avoids ongoing noise from known stuck outbounds.
		MaxRescheduledAge time.Duration `mapstructure:"max_rescheduled_age"`

		// TssKeysignFailure configures statistical alerting on tss_keysign_failure
		// events (moving window with mean + sigma * stddev thresholds).
		// MinFailures is an absolute floor on the window count, so a near-zero
		// baseline does not alert on a couple of isolated failures.
		TssKeysignFailure struct {
			WindowBlocks int64   `mapstructure:"window_blocks"`
			Sigma        float64 `mapstructure:"sigma"`
			MinSnapshots int     `mapstructure:"min_snapshots"`
			MaxSnapshots int     `mapstructure:"max_snapshots"`
			MinFailures  int     `mapstructure:"min_failures"`
		} `mapstructure:"tss_keysign_failure"`
	} `mapstructure:"thresholds"`

	// RateLimits contain alert rate limits.
	RateLimits struct {
		// FailedTransactions limits the rate of failed transaction alerts.
		FailedTransactions struct {
			// Burst is the maximum number of alerts allowed in a burst.
			Burst int `mapstructure:"burst"`

			// Interval is the minimum time between alerts (after burst is exhausted).
			Interval time.Duration `mapstructure:"interval"`
		} `mapstructure:"failed_transactions"`
	} `mapstructure:"rate_limits"`

	// Styles contain various styling for alerts.
	Styles struct {
		USDPerMoneyBag uint64 `mapstructure:"usd_per_money_bag"`
	} `mapstructure:"styles"`

	// LabeledAddresses is a map of addresses to labels.
	LabeledAddresses map[string]string `mapstructure:"labeled_addresses"`
}

////////////////////////////////////////////////////////////////////////////////////////
// Default
////////////////////////////////////////////////////////////////////////////////////////

var config = Config{}

// TODO: remove this hack if viper is updated to 1.18+
func hackBindEnv(i any, parentKey ...string) {
	v := reflect.ValueOf(i)
	v = v.Elem()
	t := v.Type()

	for j := 0; j < t.NumField(); j++ {
		field := t.Field(j)
		fieldValue := v.Field(j)

		// get the "mapstructure" tag to use as the environment variable key
		key := field.Tag.Get("mapstructure")

		// Create the full environment key including parent keys if applicable
		if len(parentKey) > 0 {
			key = parentKey[0] + "." + key
		}

		// recurse into it structs
		if fieldValue.Kind() == reflect.Struct {
			hackBindEnv(fieldValue.Addr().Interface(), key)
		} else {
			_ = viper.BindEnv(key)
		}
	}
}

func init() {
	// storage path
	config.StoragePath = "/tmp/events"

	// endpoints
	config.Endpoints.CacheSize = 100
	config.Endpoints.Thornode = "https://gateway.liquify.com/chain/thorchain_api"
	config.Endpoints.Midgard = "https://gateway.liquify.com/chain/thorchain_midgard"

	// notifications
	config.Notifications.Activity.Category = "Activity"
	config.Notifications.Info.Category = "Info"
	config.Notifications.Lending.Category = "Lending"
	config.Notifications.Security.Category = "Security"
	config.Notifications.Reschedules.Category = "Reschedules"
	config.Notifications.FailedRefunds.Category = "Failed Refunds"

	// retries
	config.MaxRetries = 10

	// network
	config.Network = "mainnet"

	// links
	config.Links.Track = "https://track.thorchain.org"
	config.Links.Explorer = "https://runescan.io"
	config.Links.Thornode = "https://gateway.liquify.com/chain/thorchain_api"

	config.TORAnchorCheckBlocks = 300 // 30 minutes

	// thresholds
	config.Thresholds.USDValue = 250_000
	config.Thresholds.RuneTransferValue = 1_000_000
	config.Thresholds.SwapDelta.USDValue = 50_000
	config.Thresholds.SwapDelta.BasisPoints = 1000
	config.Thresholds.Security.USDValue = 10_000_000
	config.Thresholds.SwapSlipBasisPoints = 100
	config.Thresholds.TORAnchorDriftBasisPoints = 500
	config.Thresholds.MaxRescheduledAge = 3 * 24 * time.Hour
	config.Thresholds.TssKeysignFailure.WindowBlocks = 720 // ~1 hour
	config.Thresholds.TssKeysignFailure.Sigma = 3.0        // 3 sigma detection
	config.Thresholds.TssKeysignFailure.MinSnapshots = 60  // ~3 days
	config.Thresholds.TssKeysignFailure.MaxSnapshots = 140 // ~7 days
	config.Thresholds.TssKeysignFailure.MinFailures = 5

	// rate limits
	config.RateLimits.FailedTransactions.Burst = 10
	config.RateLimits.FailedTransactions.Interval = time.Hour

	// styles
	config.Styles.USDPerMoneyBag = 100_000

	// labeled addresses
	// https://raw.githubusercontent.com/ViewBlock/cryptometa/master/data/thorchain/labels.json
	config.LabeledAddresses = map[string]string{
		"thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt":  "Reserve Module",
		"thor17gw75axcnr8747pkanye45pnrwk7p9c3cqncsv":  "Bond Module",
		"thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0":  "Pool Module",
		"thor1ty6h2ll07fqfzumphp6kq3hm4ps28xlm2l6kd6":  "crypto.com",
		"thor1505gp5h48zd24uexrfgka70fg8ccedafsnj0e3":  "Treasury1",
		"thor1lj62pg6ryxv2htekqx04nv7wd3g98qf9gfvamy":  "Standby Reserve",
		"thor1lrnrawjlfp6jyrzf39r740ymnuk9qgdgp29rqv":  "Vested Wallet1",
		"thor16qnm285eez48r4u9whedq4qunydu2ucmzchz7p":  "Vested Wallet2",
		"thor1egxvam70a86jafa8gcg3kqfmfax3s0m2g3m754":  "TreasuryLP",
		"thor1wfe7hsuvup27lx04p5al4zlcnx6elsnyft7dzm":  "TreasuryLP2",
		"thor14n2q7tpemxcha8zc26j0g5pksx4x3a9xw9ryq9":  "Treasury2",
		"thor1qd4my7934h2sn5ag5eaqsde39va4ex2asz3yv5":  "Treasury Multisig",
		"thor1y5lk3rzatghv9y4s4j90qt9ayq83e2dpf2hvzc":  "Vesting 9R",
		"thor1t60f02r8jvzjrhtnjgfj4ne6rs5wjnejwmj7fh":  "Binance Hot",
		"thor1cqg8pyxnq03d88cl3xfn5wzjkguw5kh9enwte4":  "Binance Cold",
		"thor1uz4fpyd5f5d6p9pzk8lxyj4qxnwq6f9utg0e7k":  "Binance",
		"thor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38cg2q6y":  "Synth Module",
		"sthor1g98cy3n9mmjrpn0sxmn63lztelera37nn2xgw3": "Pool Module",
		"sthor1dheycdevq39qlkxs2a6wuuzyn4aqxhvepe6as4": "Reserve Module",
		"sthor17gw75axcnr8747pkanye45pnrwk7p9c3ve0wxj": "Bond Module",
		"thor1nm0rrq86ucezaf8uj35pq9fpwr5r82clphp95t":  "Kraken",
	}

	// setup viper and bind to config
	hackBindEnv(&config)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	viper.AllowEmptyEnv(true)
	if err := viper.Unmarshal(&config); err != nil {
		log.Panic().Err(err).Msg("failed to unmarshal config.")
	}
}
