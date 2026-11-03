package thorclient

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// referenceFetchActiveNodes is the original, unmemoized FetchActiveNodes.
func referenceFetchActiveNodes(b *thorchainBridge) ([]common.PubKey, error) {
	na, err := b.GetNodeAccounts()
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}
	active := make([]common.PubKey, 0)
	for _, item := range na {
		if item.Status == stypes.NodeStatus_Active.String() {
			active = append(active, item.PubKeySet.Secp256k1)
		}
	}
	return active, nil
}

func assertFetchActiveNodesMatchesReference(t *testing.T, bridge *thorchainBridge, stage string) {
	t.Helper()
	want, wantErr := referenceFetchActiveNodes(bridge)
	// twice: the second call is served from the memo when the first succeeded
	for i := 0; i < 2; i++ {
		got, err := bridge.FetchActiveNodes()
		if fmt.Sprint(err) != fmt.Sprint(wantErr) {
			t.Fatalf("%s call %d: err %v, want %v", stage, i, err, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s call %d: got %v, want %v", stage, i, got, want)
		}
		if (got == nil) != (want == nil) {
			t.Fatalf("%s call %d: nil-ness differs: got %#v, want %#v", stage, i, got, want)
		}
	}
}

func TestFetchActiveNodesMatchesReference(t *testing.T) {
	setBenchBech32Prefixes()
	bodies := map[string][]byte{
		"fixture":      nodeAccountsBody(t, 2),
		"120 nodes":    nodeAccountsBody(t, 120),
		"empty list":   []byte(`[]`),
		"null":         []byte(`null`),
		"empty body":   []byte(``),
		"invalid json": []byte(`[{"status":`),
		"bad pubkey":   []byte(`[{"status":"Active","pub_key_set":{"secp256k1":"not-a-pubkey"}}]`),
	}
	// no active nodes must still return a non-nil empty slice
	bodies["none active"] = []byte(`[{"status":"Standby"}]`)

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			bridge := newNodeAccountsTestBridge("memo-" + name + ".invalid:1317")
			cacheNodeAccountsBody(t, bridge, body)
			assertFetchActiveNodesMatchesReference(t, bridge, name)
		})
	}
}

func TestFetchActiveNodesMemoFollowsHTTPCache(t *testing.T) {
	setBenchBech32Prefixes()
	bridge := newNodeAccountsTestBridge("memo-refresh.invalid:1317")

	cacheNodeAccountsBody(t, bridge, nodeAccountsBody(t, 120))
	assertFetchActiveNodesMatchesReference(t, bridge, "first body")

	// callers own the returned slice; mutating it must not leak into the memo
	got, err := bridge.FetchActiveNodes()
	if err != nil || len(got) == 0 {
		t.Fatalf("unexpected %v %v", got, err)
	}
	got[0] = "mutated"
	assertFetchActiveNodesMatchesReference(t, bridge, "after caller mutation")

	// an HTTP cache refresh with different content is decoded again
	cacheNodeAccountsBody(t, bridge, nodeAccountsBody(t, 7))
	assertFetchActiveNodesMatchesReference(t, bridge, "refreshed body")

	// a refresh with identical bytes in a new slice decodes again, same result
	cacheNodeAccountsBody(t, bridge, nodeAccountsBody(t, 7))
	assertFetchActiveNodesMatchesReference(t, bridge, "identical refreshed body")

	// errors are not memoized
	cacheNodeAccountsBody(t, bridge, []byte(`[{"status":`))
	assertFetchActiveNodesMatchesReference(t, bridge, "invalid body")
	cacheNodeAccountsBody(t, bridge, nodeAccountsBody(t, 2))
	assertFetchActiveNodesMatchesReference(t, bridge, "valid after invalid")
}

func TestFetchActiveNodesConcurrent(t *testing.T) {
	setBenchBech32Prefixes()
	bridge := newNodeAccountsTestBridge("memo-concurrent.invalid:1317")
	cacheNodeAccountsBody(t, bridge, nodeAccountsBody(t, 120))
	want, err := referenceFetchActiveNodes(bridge)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				got, err := bridge.FetchActiveNodes()
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("got %v %v", got, err)
					return
				}
				got[0] = "mutated"
			}
		}()
	}
	wg.Wait()
}
