package watchers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/test/luvicheck"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

var (
	luviFetchAttempts = 5
	luviFetchDelay    = time.Second
)

// NewLuvi checks every block that no pool loses LP value (the synth-aware
// LUVI index G) and that LP units only change through add_liquidity and
// withdraw events. A drop in a pool named by a loss event in the same block
// (errata, slash, gas, pool status or balance change) is logged, not failed.
func NewLuvi() *Watcher {
	thorscan.SetAPIEndpoint(thornodeURL)
	scanner := thorscan.Scan(1, 0)
	monitor := luvicheck.NewMonitor()

	return &Watcher{
		Name:     "Luvi",
		Interval: time.Second,
		Fn: func(config *OpConfig) error {
			for {
				select {
				case block := <-scanner:
					if err := checkLuviBlock(monitor, block); err != nil {
						return err
					}
				case <-time.After(10 * time.Millisecond):
					return nil
				}
			}
		},
	}
}

func checkLuviBlock(monitor *luvicheck.Monitor, block *thorscan.BlockResponse) error {
	cl := log.With().Str("watcher", "luvi").Logger()
	height := block.Header.Height

	pools, err := fetchLuviPools(height)
	if err != nil {
		return err
	}

	events := append([]map[string]string{}, block.BeginBlockEvents...)
	for _, tx := range block.Txs {
		events = append(events, tx.Result.Events...)
	}
	events = append(events, block.EndBlockEvents...)
	events = append(events, block.FinalizeBlockEvents...)

	violations, err := monitor.ObserveBlock(height, pools, events)
	if err != nil {
		return err
	}
	lossPools := luvicheck.LossEventPools(events)
	for _, v := range violations {
		if v.Kind == luvicheck.Drop && lossPools[v.Pool] {
			cl.Warn().Err(v).Msg("luvi drop explained by a loss event")
			continue
		}
		cl.Error().Err(v).Msg("luvi check failed")
		return v
	}
	return nil
}

func fetchLuviPools(height int64) ([]luvicheck.PoolSnapshot, error) {
	endpoint := fmt.Sprintf("%s/thorchain/pools?height=%d", thornodeURL, height)
	var err error
	for attempt := 0; attempt < luviFetchAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(luviFetchDelay)
		}
		var resp *http.Response
		resp, err = httpClient.Get(endpoint)
		if err != nil {
			continue
		}
		var pools []luvicheck.PoolSnapshot
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("pools at height %d returned status %d", height, resp.StatusCode)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&pools)
		}
		resp.Body.Close()
		if err == nil {
			return pools, nil
		}
	}
	return nil, fmt.Errorf("failed to get pools at height %d: %w", height, err)
}
