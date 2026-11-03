// mainnet-mix samples recent mainnet blocks and records which message types,
// memo types and block events dominate, plus a snapshot of the network's state
// shape (pools, nodes, vaults, queues). The output drives which code paths the
// perf benchmarks target and how the synthetic mainnet-mix workload is weighted.
//
// Usage:
//
//	LIQUIFY_API_KEY=... go run ./tools/perf/mainnet-mix -blocks 2000 -stride 25 \
//	  -out test/performance/mainnet-mix.json -shape test/performance/testdata/mainnet-shape.json
package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	endpoint = flag.String("endpoint", "https://gateway.liquify.com/chain/thorchain_api", "thornode API endpoint")
	nBlocks  = flag.Int("blocks", 2000, "number of blocks to sample")
	stride   = flag.Int("stride", 25, "sample every Nth block walking back from tip")
	workers  = flag.Int("workers", 16, "parallel fetchers")
	outPath  = flag.String("out", "test/performance/mainnet-mix.json", "mix output path")
	shape    = flag.String("shape", "test/performance/testdata/mainnet-shape.json", "state shape output path (empty to skip)")
)

var client = &http.Client{Timeout: 60 * time.Second}

// retryWait backs off between attempts (a var so tests can skip the wait)
var retryWait = func(attempt int) { time.Sleep(time.Duration(attempt+1) * time.Second) }

func get(path string, out any) error {
	u := *endpoint + path
	if key := os.Getenv("LIQUIFY_API_KEY"); key != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "api=" + url.QueryEscape(key)
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return redactURLError(path, err)
		}
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("X-Client-ID", "thornode-perf-mix")
		res, err := client.Do(req)
		if err != nil {
			lastErr = redactURLError(path, err)
			retryWait(attempt)
			continue
		}
		var body io.Reader = res.Body
		if strings.Contains(res.Header.Get("Content-Encoding"), "gzip") {
			gz, gzErr := gzip.NewReader(res.Body)
			if gzErr != nil {
				res.Body.Close()
				return gzErr
			}
			body = gz
		}
		if res.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(body, 512))
			res.Body.Close()
			lastErr = fmt.Errorf("%s: status %d: %s", path, res.StatusCode, b)
			retryWait(attempt)
			continue
		}
		err = json.NewDecoder(body).Decode(out)
		res.Body.Close()
		return err
	}
	return lastErr
}

// redactURLError drops the request URL, which carries the API key, from
// net/http errors and names the request by its key-free path instead.
func redactURLError(path string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s %s: %w", urlErr.Op, path, urlErr.Err)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// ---------------------------------------------------------------------------
// block sampling
// ---------------------------------------------------------------------------

type event map[string]any

type block struct {
	Header struct {
		Height int64  `json:"height"`
		Time   string `json:"time"`
	} `json:"header"`
	BeginBlockEvents []event `json:"begin_block_events"`
	EndBlockEvents   []event `json:"end_block_events"`
	Txs              []struct {
		Result struct {
			Code    int64   `json:"code"`
			GasUsed string  `json:"gas_used"`
			Events  []event `json:"events"`
		} `json:"result"`
		Tx *struct {
			// injected (ebifrost) txs carry messages at the top level,
			// signed txs under body
			Messages []map[string]any `json:"messages"`
			Body     struct {
				Messages []map[string]any `json:"messages"`
			} `json:"body"`
		} `json:"tx"`
	} `json:"txs"`
}

// Stat is a count plus summed gas, the gas being a proxy for keeper store work.
type Stat struct {
	Count       int64   `json:"count"`
	PerBlock    float64 `json:"per_block"`
	GasTotal    int64   `json:"gas_total,omitempty"`
	GasShare    float64 `json:"gas_share,omitempty"`
	AvgGas      int64   `json:"avg_gas,omitempty"`
	Attestation int64   `json:"attestations,omitempty"`
}

// Mix is the summarized sample.
type Mix struct {
	GeneratedAt      string           `json:"generated_at"`
	Endpoint         string           `json:"endpoint"`
	TipHeight        int64            `json:"tip_height"`
	SampledBlocks    int              `json:"sampled_blocks"`
	Stride           int              `json:"stride"`
	FirstHeight      int64            `json:"first_height"`
	LastHeight       int64            `json:"last_height"`
	TxsPerBlock      float64          `json:"txs_per_block"`
	GasPerBlock      float64          `json:"gas_per_block"`
	FailedTxs        int64            `json:"failed_txs"`
	Messages         map[string]*Stat `json:"messages"`
	Memos            map[string]*Stat `json:"memos"`
	AttestationsHist map[string]int64 `json:"attestations_histogram"`
	EndBlockEvents   map[string]*Stat `json:"end_block_events"`
	BeginBlockEvents map[string]*Stat `json:"begin_block_events"`
	TxEvents         map[string]*Stat `json:"tx_events"`
}

func memoClass(memo string) string {
	if memo == "" {
		return "empty"
	}
	parts := strings.Split(memo, ":")
	p := strings.ToLower(strings.TrimSpace(parts[0]))
	switch p {
	case "=", "s", "swap":
		cls := "swap"
		if len(parts) > 3 && strings.Contains(parts[3], "/") {
			cls = "swap-streaming"
		}
		return cls
	case "=<", "=>":
		return "swap-limit"
	case "out":
		return "outbound"
	case "refund":
		return "refund"
	case "+", "a", "add":
		return "add-liquidity"
	case "-", "wd", "withdraw":
		return "withdraw-liquidity"
	case "migrate":
		return "migrate"
	case "consolidate":
		return "consolidate"
	case "noop":
		return "noop"
	case "bond", "unbond", "leave":
		return p
	case "~", "n", "name":
		return "thorname"
	case "trade+", "trade-":
		return p
	case "secure+", "secure-":
		return p
	case "pool+", "pool-":
		return "runepool"
	case "x", "exec":
		return "exec"
	case "tcy", "tcy+", "tcy-":
		return "tcy"
	case "ragnarok", "yggdrasil+", "yggdrasil-", "reserve", "donate", "d":
		return p
	}
	if len(p) > 12 {
		return "other"
	}
	return "other:" + p
}

func add(m map[string]*Stat, key string, gas int64) *Stat {
	s, ok := m[key]
	if !ok {
		s = &Stat{}
		m[key] = s
	}
	s.Count++
	s.GasTotal += gas
	return s
}

func attestBucket(n int) string {
	switch {
	case n <= 1:
		return "1"
	case n <= 5:
		return "2-5"
	case n <= 20:
		return "6-20"
	case n <= 50:
		return "21-50"
	default:
		return "51+"
	}
}

func sampleBlocks() (*Mix, error) {
	var tip block
	if err := get("/thorchain/block", &tip); err != nil {
		return nil, fmt.Errorf("fetch tip: %w", err)
	}
	heights := make([]int64, 0, *nBlocks)
	for i := 0; i < *nBlocks; i++ {
		h := tip.Header.Height - int64(i**stride)
		if h <= 0 {
			break
		}
		heights = append(heights, h)
	}

	jobs := make(chan int64)
	results := make(chan *block)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for h := range jobs {
				var b block
				if err := get("/thorchain/block?height="+strconv.FormatInt(h, 10), &b); err != nil {
					fmt.Fprintf(os.Stderr, "skip block %d: %v\n", h, err)
					continue
				}
				results <- &b
			}
		}()
	}
	go func() {
		for _, h := range heights {
			jobs <- h
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(results) }()

	mix := &Mix{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		Endpoint:         *endpoint,
		TipHeight:        tip.Header.Height,
		Stride:           *stride,
		Messages:         map[string]*Stat{},
		Memos:            map[string]*Stat{},
		AttestationsHist: map[string]int64{},
		EndBlockEvents:   map[string]*Stat{},
		BeginBlockEvents: map[string]*Stat{},
		TxEvents:         map[string]*Stat{},
	}
	var totalTxs, totalGas int64
	done := 0
	for b := range results {
		done++
		if done%100 == 0 {
			fmt.Fprintf(os.Stderr, "sampled %d/%d blocks\n", done, len(heights))
		}
		if mix.FirstHeight == 0 || b.Header.Height < mix.FirstHeight {
			mix.FirstHeight = b.Header.Height
		}
		if b.Header.Height > mix.LastHeight {
			mix.LastHeight = b.Header.Height
		}
		for _, e := range b.EndBlockEvents {
			add(mix.EndBlockEvents, fmt.Sprint(e["type"]), 0)
		}
		for _, e := range b.BeginBlockEvents {
			add(mix.BeginBlockEvents, fmt.Sprint(e["type"]), 0)
		}
		for _, tx := range b.Txs {
			totalTxs++
			gas, _ := strconv.ParseInt(tx.Result.GasUsed, 10, 64)
			totalGas += gas
			if tx.Result.Code != 0 {
				mix.FailedTxs++
			}
			for _, e := range tx.Result.Events {
				add(mix.TxEvents, fmt.Sprint(e["type"]), 0)
			}
			var msgs []map[string]any
			if tx.Tx != nil {
				msgs = tx.Tx.Messages
				if len(msgs) == 0 {
					msgs = tx.Tx.Body.Messages
				}
			}
			if len(msgs) == 0 {
				// undecodable tx: fall back to the message action event
				action := "unknown"
				for _, e := range tx.Result.Events {
					if e["type"] == "message" && e["action"] != nil {
						action = fmt.Sprint(e["action"])
						break
					}
				}
				add(mix.Messages, action, gas)
				continue
			}
			// attribute the tx gas evenly across its messages
			msgGas := gas / int64(len(msgs))
			for _, m := range msgs {
				typ := fmt.Sprint(m["@type"])
				s := add(mix.Messages, typ, msgGas)
				switch typ {
				case "/types.MsgObservedTxQuorum":
					q, _ := m["quoTx"].(map[string]any)
					obs, _ := q["obsTx"].(map[string]any)
					txm, _ := obs["tx"].(map[string]any)
					att, _ := q["attestations"].([]any)
					s.Attestation += int64(len(att))
					mix.AttestationsHist[attestBucket(len(att))]++
					dir := "out"
					if in, _ := q["inbound"].(bool); in {
						dir = "in"
					}
					add(mix.Memos, "observed-"+dir+":"+memoClass(fmt.Sprint(txm["memo"])), msgGas)
				case "/types.MsgDeposit":
					add(mix.Memos, "deposit:"+memoClass(fmt.Sprint(m["memo"])), msgGas)
				case "/types.MsgObservedTxIn", "/types.MsgObservedTxOut":
					txs, _ := m["txs"].([]any)
					for _, t := range txs {
						o, _ := t.(map[string]any)
						txm, _ := o["tx"].(map[string]any)
						add(mix.Memos, "legacy-observed:"+memoClass(fmt.Sprint(txm["memo"])), msgGas/int64(len(txs)))
					}
				}
			}
		}
	}
	mix.SampledBlocks = done
	if done == 0 {
		return nil, fmt.Errorf("no blocks sampled")
	}
	nb := float64(done)
	mix.TxsPerBlock = float64(totalTxs) / nb
	mix.GasPerBlock = float64(totalGas) / nb
	for _, m := range []map[string]*Stat{mix.Messages, mix.Memos, mix.EndBlockEvents, mix.BeginBlockEvents, mix.TxEvents} {
		for _, s := range m {
			s.PerBlock = float64(s.Count) / nb
			if totalGas > 0 && s.GasTotal > 0 {
				s.GasShare = float64(s.GasTotal) / float64(totalGas)
				s.AvgGas = s.GasTotal / s.Count
			}
		}
	}
	return mix, nil
}

// ---------------------------------------------------------------------------
// state shape
// ---------------------------------------------------------------------------

type Shape struct {
	Height            int64            `json:"height"`
	Pools             []map[string]any `json:"pools"`
	NodesByStatus     map[string]int   `json:"nodes_by_status"`
	AsgardVaults      []map[string]any `json:"asgard_vaults"`
	Mimir             map[string]int64 `json:"mimir"`
	SwapQueueLen      int              `json:"swap_queue_len"`
	OutboundQueueLen  int              `json:"outbound_queue_len"`
	ScheduledOutbound int              `json:"scheduled_outbound_len"`
	StreamingSwaps    int              `json:"streaming_swaps"`
	LimitSwaps        int              `json:"limit_swaps"`
}

func fetchShape() (*Shape, error) {
	s := &Shape{NodesByStatus: map[string]int{}}
	var tip block
	if err := get("/thorchain/block", &tip); err == nil {
		s.Height = tip.Header.Height
	}

	var pools []map[string]any
	if err := get("/thorchain/pools", &pools); err != nil {
		return nil, err
	}
	for _, p := range pools {
		s.Pools = append(s.Pools, pick(p, "asset", "status", "balance_rune", "balance_asset", "pool_units", "LP_units", "synth_units", "decimals"))
	}

	var nodes []map[string]any
	if err := get("/thorchain/nodes", &nodes); err != nil {
		return nil, err
	}
	for _, n := range nodes {
		s.NodesByStatus[fmt.Sprint(n["status"])]++
	}

	var vaults []map[string]any
	if err := get("/thorchain/vaults/asgard", &vaults); err != nil {
		return nil, err
	}
	for _, v := range vaults {
		members, _ := v["membership"].([]any)
		coins, _ := v["coins"].([]any)
		routers, _ := v["routers"].([]any)
		addrs, _ := v["addresses"].([]any)
		s.AsgardVaults = append(s.AsgardVaults, map[string]any{
			"status":     v["status"],
			"members":    len(members),
			"coins":      len(coins),
			"routers":    len(routers),
			"chains":     len(addrs),
			"pending_tx": v["pending_tx_block_heights"],
		})
	}

	if err := get("/thorchain/mimir", &s.Mimir); err != nil {
		return nil, err
	}

	var swaps []any
	if err := get("/thorchain/queue/swap", &swaps); err == nil {
		s.SwapQueueLen = len(swaps)
	}
	var outbound []any
	if err := get("/thorchain/queue/outbound", &outbound); err == nil {
		s.OutboundQueueLen = len(outbound)
	}
	var scheduled []any
	if err := get("/thorchain/queue/scheduled", &scheduled); err == nil {
		s.ScheduledOutbound = len(scheduled)
	}
	var streaming []any
	if err := get("/thorchain/swaps/streaming", &streaming); err == nil {
		s.StreamingSwaps = len(streaming)
	}
	var limit struct {
		LimitSwaps []any `json:"limit_swaps"`
	}
	if err := get("/thorchain/queue/limit_swaps", &limit); err == nil {
		s.LimitSwaps = len(limit.LimitSwaps)
	}
	return s, nil
}

func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// output
// ---------------------------------------------------------------------------

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printTable(title string, m map[string]*Stat, limit int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := m[keys[i]], m[keys[j]]
		if a.GasTotal != b.GasTotal {
			return a.GasTotal > b.GasTotal
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return keys[i] < keys[j]
	})
	fmt.Printf("\n### %s\n\n| key | per block | gas share | avg gas |\n|---|---:|---:|---:|\n", title)
	for i, k := range keys {
		if i >= limit {
			break
		}
		s := m[k]
		fmt.Printf("| %s | %.2f | %.1f%% | %d |\n", k, s.PerBlock, s.GasShare*100, s.AvgGas)
	}
}

func main() {
	flag.Parse()
	if os.Getenv("LIQUIFY_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "warning: LIQUIFY_API_KEY not set; public rate limits apply")
	}

	mix, err := sampleBlocks()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = writeJSON(*outPath, mix); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("## Mainnet mix: %d blocks (stride %d, heights %d-%d)\n\n", mix.SampledBlocks, mix.Stride, mix.FirstHeight, mix.LastHeight)
	fmt.Printf("txs/block %.2f, gas/block %.0f, failed txs %d\n", mix.TxsPerBlock, mix.GasPerBlock, mix.FailedTxs)
	printTable("Messages", mix.Messages, 30)
	printTable("Memo classes", mix.Memos, 30)
	printTable("EndBlock events", mix.EndBlockEvents, 25)
	fmt.Printf("\nattestations per quorum msg: %v\n", mix.AttestationsHist)

	if *shape != "" {
		s, err := fetchShape()
		if err != nil {
			fmt.Fprintln(os.Stderr, "shape:", err)
			os.Exit(1)
		}
		if err = writeJSON(*shape, s); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("\nshape: %d pools, nodes %v, %d asgard vaults, swap queue %d, streaming %d, limit %d\n",
			len(s.Pools), s.NodesByStatus, len(s.AsgardVaults), s.SwapQueueLen, s.StreamingSwaps, s.LimitSwaps)
	}
}
