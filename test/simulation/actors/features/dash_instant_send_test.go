package features

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type instantSendTestBridge struct {
	thorclient.ThorchainBridge
	values     map[string]int64
	errors     map[string]error
	reads      []string
	beforeRead func()
}

// GetMimir models the per-key endpoint, including an unset key's negative value.
func (b *instantSendTestBridge) GetMimir(key string) (int64, error) {
	if b.beforeRead != nil {
		b.beforeRead()
	}
	b.reads = append(b.reads, key)
	if err := b.errors[key]; err != nil {
		return 0, err
	}
	if value, ok := b.values[key]; ok {
		return value, nil
	}
	return -1, nil
}

// newInstantSendTestActor provides a ready node with the mocknet allowance default.
func newInstantSendTestActor() (*DashInstantSendActor, *instantSendTestBridge) {
	bridge := &instantSendTestBridge{
		values: map[string]int64{"DashInstantSendMaxAmount": 1000 * common.One},
		errors: map[string]error{},
	}
	actor := &DashInstantSendActor{
		getMimir: bridge.GetMimir,
		readMetrics: func() (instantSendMetrics, error) {
			return instantSendMetrics{available: 1000 * common.One}, nil
		},
	}
	return actor, bridge
}

func TestInstantSendReadinessAmountSettings(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     int64
		wantError string
	}{
		{"mocknet defaults", "", 0, ""},
		{"enabled", "DashInstantSendEnabled", 1, ""},
		{"disabled", "DashInstantSendEnabled", 0, "explicitly disables"},
		{"unset maximum", "DashInstantSendMaxAmount", -1, "must be positive"},
		{"zero maximum", "DashInstantSendMaxAmount", 0, "must be positive"},
		{"insufficient maximum", "DashInstantSendMaxAmount", dashInstantSendLargePayment - 1, "cannot cover"},
		{"exact maximum", "DashInstantSendMaxAmount", dashInstantSendLargePayment, ""},
		{"minimum too high", "DashInstantSendMinAmount", dashInstantSendSmallPayment + 1, "exceeds payment"},
		{"exact minimum", "DashInstantSendMinAmount", dashInstantSendSmallPayment, ""},
		{"minimum disabled", "DashInstantSendMinAmount", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			bridge.values[tc.key] = tc.value
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			result := a.waitForReadiness(config)
			if tc.wantError == "" {
				if !result.Continue || result.Finish || result.Error != nil {
					t.Fatalf("valid settings rejected: %+v", result)
				}
			} else if !result.Finish || result.Continue || result.Error == nil || !strings.Contains(result.Error.Error(), tc.wantError) {
				t.Fatalf("expected immediate failure containing %q, got %+v", tc.wantError, result)
			}
			if a.account != nil {
				t.Fatal("readiness acquired a user account")
			}
		})
	}
}

func TestInstantSendReadinessDefaultMinimum(t *testing.T) {
	a, bridge := newInstantSendTestActor()
	retry, err := a.checkReadiness(common.One/100-1, common.One/100-1)
	if err == nil || retry {
		t.Fatalf("unset minimum must reject a deposit below 0.01 DASH: retry=%v, err=%v", retry, err)
	}
	bridge.values["DashInstantSendMinAmount"] = 0
	_, err = a.checkReadiness(1, 1)
	if err != nil {
		t.Fatalf("zero minimum must allow small deposits: %v", err)
	}
}

func TestInstantSendReadinessRetriesLookupFailures(t *testing.T) {
	keys := []string{"DashInstantSendEnabled", "DashInstantSendMinAmount", "DashInstantSendMaxAmount"}
	for i, key := range keys {
		t.Run(key, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			failed := errors.New("node unavailable")
			bridge.errors[key] = failed
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			result := a.waitForReadiness(config)
			if result.Finish || result.Continue || !errors.Is(result.Error, failed) {
				t.Fatalf("lookup failure must retry: %+v", result)
			}
			if len(bridge.reads) != i+1 {
				t.Fatalf("made more lookups after failure: %v", bridge.reads)
			}
			delete(bridge.errors, key)
			result = a.waitForReadiness(config)
			if !result.Continue || result.Error != nil {
				t.Fatalf("did not recover: %+v", result)
			}
		})
	}
}

func TestInstantSendReadinessWarmupAndAllowance(t *testing.T) {
	a, bridge := newInstantSendTestActor()
	config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
	state := instantSendMetrics{warmingUp: true, available: 1000 * common.One}
	var scrapeErr error = errors.New("metrics unavailable")
	a.readMetrics = func() (instantSendMetrics, error) { return state, scrapeErr }
	for _, reason := range []string{"metrics unavailable", "warming up", "available allowance"} {
		result := a.waitForReadiness(config)
		if result.Finish || result.Continue || result.Error == nil || !strings.Contains(result.Error.Error(), reason) {
			t.Fatalf("expected retry for %s: %+v", reason, result)
		}
		if scrapeErr != nil {
			scrapeErr = nil
		} else {
			state.warmingUp = false
			state.available = dashInstantSendLargePayment - 1
		}
	}
	state.available = dashInstantSendLargePayment
	result := a.waitForReadiness(config)
	if !result.Continue || result.Error != nil {
		t.Fatalf("exactly enough allowance must be ready: %+v", result)
	}
}

func TestInstantSendReadinessTimeout(t *testing.T) {
	for _, reason := range []string{"warming up", "metrics unavailable", "node unavailable"} {
		t.Run(reason, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			a.readMetrics = func() (instantSendMetrics, error) {
				if reason == "metrics unavailable" {
					return instantSendMetrics{}, errors.New(reason)
				}
				return instantSendMetrics{warmingUp: true}, nil
			}
			if reason == "node unavailable" {
				bridge.errors["DashInstantSendEnabled"] = errors.New(reason)
			}
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			result := a.waitForReadiness(config)
			if result.Finish || result.Continue || result.Error == nil {
				t.Fatalf("temporary failure must retry first: %+v", result)
			}
			a.readyDeadline = time.Now().Add(-time.Second)
			result = a.waitForReadiness(config)
			if !result.Finish || result.Error == nil || !strings.Contains(result.Error.Error(), "timed out") || !strings.Contains(result.Error.Error(), reason) {
				t.Fatalf("timeout must include the last failure: %+v", result)
			}
		})
	}
}

func TestInstantSendReadinessTimeoutWithStalledLookup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, bridge := newInstantSendTestActor()
		pending := make(chan struct{})
		defer close(pending)
		bridge.beforeRead = func() { <-pending }
		started := time.Now()
		result := a.waitForReadiness(&OpConfig{NodeUsers: []*User{{Thorchain: bridge}}})
		if !result.Finish || result.Error == nil || !strings.Contains(result.Error.Error(), "lookup did not finish") {
			t.Fatalf("stalled lookup did not time out: %+v", result)
		}
		if elapsed := time.Since(started); elapsed != 2*time.Minute {
			t.Fatalf("readiness deadline took %s instead of two minutes", elapsed)
		}
	})
}

func TestInstantSendReadinessRunsBeforeAcquiringAccount(t *testing.T) {
	a := DashInstantSend(rand.New(rand.NewPCG(1, 2)))
	if !strings.Contains(a.Ops[0].String(), "waitForReadiness") || a.Interval != time.Second {
		t.Fatal("readiness must be the first operation, retried once per second")
	}
	for _, config := range []*OpConfig{nil, {}, {NodeUsers: []*User{nil}}, {NodeUsers: []*User{{}}}} {
		result := a.Ops[0](config)
		if !result.Finish || result.Error == nil {
			t.Fatalf("missing node bridge must fail clearly: %+v", result)
		}
	}
}

type instantSendTestChainClient struct {
	LiteChainClient
	signed, broadcast int
}

// SignTx records whether a readiness failure allowed funds to be signed.
func (c *instantSendTestChainClient) SignTx(SimTx) ([]byte, error) {
	c.signed++
	return []byte("signed"), nil
}

// BroadcastTx records whether a readiness failure allowed funds to be sent.
func (c *instantSendTestChainClient) BroadcastTx([]byte) (string, error) {
	c.broadcast++
	return "txid", nil
}

func TestInstantSendBroadcastRechecksReadiness(t *testing.T) {
	for _, failure := range []string{"disabled", "maximum lowered", "minimum raised", "lookup failure", "warming up", "allowance exhausted", "scrape failure"} {
		t.Run(failure, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			chain := &instantSendTestChainClient{}
			a.account = &User{ChainClients: map[common.Chain]LiteChainClient{common.DASHChain: chain}}
			_, err := a.checkReadiness(dashInstantSendSmallPayment, dashInstantSendLargePayment)
			if err != nil {
				t.Fatal(err)
			}
			// Change policy or readiness after startup succeeded.
			switch failure {
			case "disabled":
				bridge.values["DashInstantSendEnabled"] = 0
			case "maximum lowered":
				bridge.values["DashInstantSendMaxAmount"] = 1
			case "minimum raised":
				bridge.values["DashInstantSendMinAmount"] = dashInstantSendLargePayment + 1
			case "lookup failure":
				bridge.errors["DashInstantSendEnabled"] = errors.New("lookup failed")
			default:
				a.readMetrics = func() (instantSendMetrics, error) {
					if failure == "scrape failure" {
						return instantSendMetrics{}, errors.New("scrape failed")
					}
					return instantSendMetrics{warmingUp: failure == "warming up"}, nil
				}
			}
			_, err = a.broadcastTx("test", dashInstantSendSmallPayment, 0)
			if err == nil || chain.signed != 0 || chain.broadcast != 0 {
				t.Fatalf("unready actor spent funds: signed=%d, broadcast=%d, err=%v", chain.signed, chain.broadcast, err)
			}
		})
	}
}

func TestInstantSendBroadcastWhenReady(t *testing.T) {
	a, _ := newInstantSendTestActor()
	chain := &instantSendTestChainClient{}
	a.account = &User{ChainClients: map[common.Chain]LiteChainClient{common.DASHChain: chain}}
	a.readMetrics = func() (instantSendMetrics, error) {
		return instantSendMetrics{available: dashInstantSendLargePayment}, nil
	}
	id, err := a.broadcastTx("test", dashInstantSendSmallPayment, 0)
	if err != nil || id != "txid" || chain.signed != 1 || chain.broadcast != 1 {
		t.Fatalf("ready actor did not broadcast: id=%s, err=%v", id, err)
	}
}
