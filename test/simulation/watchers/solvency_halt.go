package watchers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

var (
	solvencyHaltRecheckAttempts = 2
	solvencyHaltRecheckDelay    = 2 * time.Second
)

type outboundQueueItem struct {
	Chain        string `json:"chain"`
	Memo         string `json:"memo"`
	OriginalMemo string `json:"original_memo"`
}

func fetchActiveSolvencyHalts(cl *http.Client, endpoint string) (map[string]int64, error) {
	mimirRes, err := fetchMimir(cl, endpoint)
	if err != nil {
		return nil, err
	}

	activeHalts := make(map[string]int64)
	for k, v := range mimirRes {
		if strings.HasPrefix(k, "SOLVENCYHALT") && v > 0 {
			activeHalts[k] = v
		}
	}

	return activeHalts, nil
}

func fetchMimir(cl *http.Client, endpoint string) (map[string]int64, error) {
	resp, err := cl.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mimir response returned non-200 status: %d", resp.StatusCode)
	}

	mimirRes := make(map[string]int64)
	if err = json.NewDecoder(resp.Body).Decode(&mimirRes); err != nil {
		return nil, err
	}

	return mimirRes, nil
}

func fetchPendingRagnarokChains(cl *http.Client, endpoint string) (map[string]struct{}, error) {
	resp, err := cl.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("outbound queue response returned non-200 status: %d", resp.StatusCode)
	}

	var outboundItems []outboundQueueItem
	if err = json.NewDecoder(resp.Body).Decode(&outboundItems); err != nil {
		return nil, err
	}

	chains := make(map[string]struct{})
	for _, item := range outboundItems {
		memo := item.OriginalMemo
		if memo == "" {
			memo = item.Memo
		}
		if strings.HasPrefix(memo, "RAGNAROK:") {
			chains[item.Chain] = struct{}{}
		}
	}

	return chains, nil
}

func fetchActiveRagnarokChains(cl *http.Client, endpoint string) (map[string]struct{}, error) {
	mimirRes, err := fetchMimir(cl, endpoint)
	if err != nil {
		return nil, err
	}

	chains := make(map[string]struct{})
	for k, v := range mimirRes {
		if v <= 0 || !strings.HasPrefix(k, "RAGNAROK-") {
			continue
		}
		asset := strings.TrimPrefix(k, "RAGNAROK-")
		chain, _, _ := strings.Cut(asset, "-")
		if chain != "" {
			chains[chain] = struct{}{}
		}
	}

	return chains, nil
}

// fetchRagnarokInProgress checks the network-level ragnarok endpoint because
// solvency halts can be raised during the drain before per-chain ragnarok Mimir
// and outbound queue state are visible to the watcher.
func fetchRagnarokInProgress(cl *http.Client, endpoint string) (bool, error) {
	resp, err := cl.Get(endpoint)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ragnarok response returned non-200 status: %d", resp.StatusCode)
	}

	var inProgress bool
	if err = json.NewDecoder(resp.Body).Decode(&inProgress); err != nil {
		return false, err
	}
	return inProgress, nil
}

func haltedChains(activeHalts map[string]int64) []string {
	chains := make([]string, 0, len(activeHalts))
	for k := range activeHalts {
		chain := strings.TrimPrefix(k, "SOLVENCYHALT")
		chain = strings.TrimSuffix(chain, "CHAIN")
		if chain == "" {
			continue
		}
		chains = append(chains, chain)
	}
	sort.Strings(chains)
	return chains
}

func allHaltedChainsInSet(activeHalts map[string]int64, coveredChains map[string]struct{}) bool {
	halted := haltedChains(activeHalts)
	if len(halted) == 0 {
		return false
	}
	for _, chain := range halted {
		if _, ok := coveredChains[chain]; !ok {
			return false
		}
	}
	return true
}

func NewSolvencyHalt() *Watcher {
	cl := log.With().Str("watcher", "solvency").Logger()

	return &Watcher{
		Name:     "SolvencyHalt",
		Interval: time.Second,
		Fn: func(config *OpConfig) error {
			endpoint := fmt.Sprintf("%s/thorchain/mimir", thornodeURL)
			queueEndpoint := fmt.Sprintf("%s/thorchain/queue/outbound", thornodeURL)
			ragnarokEndpoint := fmt.Sprintf("%s/thorchain/ragnarok", thornodeURL)

			activeHalts, err := fetchActiveSolvencyHalts(httpClient, endpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get mimir response")
				return nil
			}
			if len(activeHalts) == 0 {
				return nil
			}

			transient := false
			for attempt := 0; attempt < solvencyHaltRecheckAttempts; attempt++ {
				time.Sleep(solvencyHaltRecheckDelay)

				recheck, recheckErr := fetchActiveSolvencyHalts(httpClient, endpoint)
				if recheckErr != nil {
					cl.Error().Err(recheckErr).
						Int("attempt", attempt+1).
						Msg("failed to recheck mimir response")
					continue
				}
				if len(recheck) == 0 {
					transient = true
					cl.Warn().Int("attempt", attempt+1).Msg("transient solvency halt cleared on recheck")
					break
				}
				activeHalts = recheck
			}
			if transient {
				return nil
			}

			// During protocol ragnarok, solvency Mimir can be raised while funds
			// are actively draining and before the outbound queue/per-chain
			// ragnarok Mimir checks below see the same state. Treat the network
			// ragnarok endpoint as the source of truth for that transition window.
			networkRagnarok, err := fetchRagnarokInProgress(httpClient, ragnarokEndpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get ragnarok response")
			} else if networkRagnarok {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while network ragnarok drains")
				return nil
			}

			pendingRagnarokChains, err := fetchPendingRagnarokChains(httpClient, queueEndpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get outbound queue response")
			} else if allHaltedChainsInSet(activeHalts, pendingRagnarokChains) {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while pending ragnarok outbounds drain")
				return nil
			}

			activeRagnarokChains, err := fetchActiveRagnarokChains(httpClient, endpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get ragnarok mimir response")
			} else if allHaltedChainsInSet(activeHalts, activeRagnarokChains) {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while active ragnarok drains")
				return nil
			}

			keys := make([]string, 0, len(activeHalts))
			for k := range activeHalts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			key := keys[0]
			value := activeHalts[key]
			cl.Error().Str("mimir", key).Int64("value", value).Msg("solvency halt detected")
			return fmt.Errorf("solvency halt detected: %s=%d", key, value)
		},
	}
}
