package thorclient

import (
	"encoding/json"
	"fmt"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// GetNodeAccount retrieves node account for this address from thorchain
func (b *thorchainBridge) GetNodeAccount(thorAddr string) (*types.NodeAccount, error) {
	path := fmt.Sprintf("%s/%s", NodeAccountEndpoint, thorAddr)
	body, _, err := b.getWithPath(path)
	if err != nil {
		return &types.NodeAccount{}, fmt.Errorf("failed to get node account: %w", err)
	}
	var na types.NodeAccount
	if err = json.Unmarshal(body, &na); err != nil {
		return &types.NodeAccount{}, fmt.Errorf("failed to unmarshal node account: %w", err)
	}
	return &na, nil
}

// GetNodeAccounts retrieves all node accounts from thorchain
// Returns QueryNodeResponse which includes PreflightStatus for each node
func (b *thorchainBridge) GetNodeAccounts() ([]*types.QueryNodeResponse, error) {
	body, err := b.getNodeAccountsBody()
	if err != nil {
		return nil, err
	}
	return decodeNodeAccounts(body)
}

// getNodeAccountsBody returns the node accounts response, which get serves
// from the shared HTTP cache for up to one THORChain block.
func (b *thorchainBridge) getNodeAccountsBody() ([]byte, error) {
	body, _, err := b.getWithPath(NodeAccountsEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to get node accounts: %w", err)
	}
	return body, nil
}

func decodeNodeAccounts(body []byte) ([]*types.QueryNodeResponse, error) {
	var nodes []*types.QueryNodeResponse
	if err := json.Unmarshal(body, &nodes); err != nil {
		return nil, fmt.Errorf("failed to unmarshal node accounts: %w", err)
	}
	return nodes, nil
}

// activeNodesMemo holds FetchActiveNodes' result for one node accounts
// response. Until the HTTP cache refreshes, get returns the very same body
// slice and nothing writes to a cached body, so the same slice means the same
// bytes and therefore the same result. The body is retained so its backing
// array cannot be reused for a different response.
type activeNodesMemo struct {
	mu     sync.Mutex
	body   []byte
	active []common.PubKey
}

func sameSlice(a, b []byte) bool {
	return len(a) > 0 && len(a) == len(b) && &a[0] == &b[0]
}

// get returns a copy of the memoized active nodes if body is the memoized one.
func (m *activeNodesMemo) get(body []byte) ([]common.PubKey, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !sameSlice(m.body, body) {
		return nil, false
	}
	return append(make([]common.PubKey, 0, len(m.active)), m.active...), true
}

func (m *activeNodesMemo) set(body []byte, active []common.PubKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.body = body
	m.active = append(make([]common.PubKey, 0, len(active)), active...)
}
