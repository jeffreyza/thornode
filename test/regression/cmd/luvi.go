package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"

	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/luvicheck"
)

////////////////////////////////////////////////////////////////////////////////////////
// LUVI
////////////////////////////////////////////////////////////////////////////////////////

// Every create-blocks op checks, block by block, that no pool loses LP value
// (the synth-aware LUVI index G) and that LP units only change through
// add_liquidity and withdraw events. Pools listed in the op's luvi_drop are
// expected to lose value and must do so at least once during the op.

var (
	luviMonitors   = map[int]*luvicheck.Monitor{}
	luviMonitorsMu = sync.Mutex{}
)

func resetLuviMonitor(routine int) {
	luviMonitorsMu.Lock()
	defer luviMonitorsMu.Unlock()
	luviMonitors[routine] = luvicheck.NewMonitor()
}

func luviMonitor(routine int) *luvicheck.Monitor {
	luviMonitorsMu.Lock()
	defer luviMonitorsMu.Unlock()
	return luviMonitors[routine]
}

// observeLuvi feeds the pools as of height (latest when 0), and the events of
// that block, to the routine's monitor.
func observeLuvi(routine int, height int64, block *openapi.BlockResponse) ([]luvicheck.Violation, error) {
	url := fmt.Sprintf("http://localhost:%d/thorchain/pools", 1317+routine)
	if height > 0 {
		url += fmt.Sprintf("?height=%d", height)
	}
	var res *http.Response
	var err error
	for j := 0; j < 5; j++ {
		res, err = httpClient.Get(url)
		if err == nil {
			if res.StatusCode == http.StatusOK {
				break
			}
			_ = res.Body.Close()
			err = fmt.Errorf("status %d", res.StatusCode)
		}
		time.Sleep(100 * time.Millisecond * getTimeFactor())
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get pools at height %d: %w", height, err)
	}
	defer res.Body.Close()

	var pools []luvicheck.PoolSnapshot
	if err = json.NewDecoder(res.Body).Decode(&pools); err != nil {
		return nil, fmt.Errorf("failed to decode pools at height %d: %w", height, err)
	}

	events := append([]map[string]string{}, block.BeginBlockEvents...)
	for _, tx := range block.Txs {
		events = append(events, tx.Result.Events...)
	}
	events = append(events, block.EndBlockEvents...)
	events = append(events, block.FinalizeBlockEvents...)

	return luviMonitor(routine).ObserveBlock(height, pools, events)
}

// checkLuvi fails on any ledger mismatch, on a drop in a pool not listed in
// expectedDrops, and on a listed pool that never dropped.
func checkLuvi(violations []luvicheck.Violation, expectedDrops []string) error {
	expected := map[string]bool{}
	for _, pool := range expectedDrops {
		expected[strings.ToUpper(pool)] = true
	}

	var returnErr error
	dropped := map[string]bool{}
	for _, v := range violations {
		if v.Kind == luvicheck.Drop && expected[v.Pool] {
			dropped[v.Pool] = true
			continue
		}
		returnErr = multierror.Append(returnErr, v)
	}

	missing := []string{}
	for pool := range expected {
		if !dropped[pool] {
			missing = append(missing, pool)
		}
	}
	sort.Strings(missing)
	for _, pool := range missing {
		returnErr = multierror.Append(returnErr, fmt.Errorf("luvi: expected a drop in %s but none occurred (remove it from luvi_drop)", pool))
	}
	return returnErr
}
