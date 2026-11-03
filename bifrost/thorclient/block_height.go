package thorclient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

// GetLastObservedInHeight returns the lastobservedin value for the chain past in
func (b *thorchainBridge) GetLastObservedInHeight(chain common.Chain) (int64, error) {
	lastblock, err := b.getLastBlock(chain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetLastObservedInHeight: %w", err)
	}
	for _, item := range lastblock {
		if item.Chain == chain.String() {
			return item.LastObservedIn, nil
		}
	}
	return 0, fmt.Errorf("fail to GetLastObservedInHeight,chain(%s)", chain)
}

// GetLastObservedInHeightAtHeight returns the agreed external-chain height from
// one committed THORChain block. Signing must not fall back to latest state if
// this historical state is unavailable or the server ignores the height query.
func (b *thorchainBridge) GetLastObservedInHeightAtHeight(chain common.Chain, thorchainHeight int64) (int64, error) {
	if chain.IsEmpty() || thorchainHeight <= 0 {
		return 0, fmt.Errorf("invalid historical lastblock query: chain %s, height %d", chain, thorchainHeight)
	}
	lastblock, err := b.getLastBlockWithPath(chain, func(path string) ([]byte, int, error) {
		// Avoid retaining a separate entry in the shared response cache for
		// every signing height.
		endpoint, err := url.Parse(b.getThorChainURL(strings.TrimPrefix(path, "/")))
		if err != nil {
			return nil, 0, err
		}
		query := endpoint.Query()
		query.Set("height", strconv.FormatInt(thorchainHeight, 10))
		endpoint.RawQuery = query.Encode()
		return b.getUncached(endpoint.String())
	})
	if err != nil {
		return 0, fmt.Errorf("get last observed %s height at THORChain height %d: %w", chain, thorchainHeight, err)
	}
	for _, item := range lastblock {
		if item.Chain != chain.String() {
			continue
		}
		if item.Thorchain != thorchainHeight || item.LastObservedIn <= 0 {
			return 0, fmt.Errorf("invalid lastblock response for %s at THORChain height %d: height %d, last observed %d", chain, thorchainHeight, item.Thorchain, item.LastObservedIn)
		}
		return item.LastObservedIn, nil
	}
	return 0, fmt.Errorf("missing lastblock response for %s at THORChain height %d", chain, thorchainHeight)
}

// GetLastSignedOutHeight returns the lastsignedout value for thorchain
func (b *thorchainBridge) GetLastSignedOutHeight(chain common.Chain) (int64, error) {
	lastblock, err := b.getLastBlock(chain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetLastSignedOutHeight: %w", err)
	}
	for _, item := range lastblock {
		if item.Chain == chain.String() {
			return item.LastSignedOut, nil
		}
	}
	return 0, fmt.Errorf("fail to GetLastSignedOutHeight,chain(%s)", chain)
}

// GetBlockHeight returns the current height for thorchain blocks
func (b *thorchainBridge) GetBlockHeight() (int64, error) {
	latestBlocks, err := b.getLastBlock(common.EmptyChain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetThorchainHeight: %w", err)
	}
	for _, item := range latestBlocks {
		return item.Thorchain, nil
	}
	return 0, fmt.Errorf("failed to GetThorchainHeight")
}

// GetBlockHeightUncached returns the current THORChain height from a fresh HTTP
// response. It bypasses the shared one-block cache so callers can use it as a
// visibility barrier before making irreversible local security decisions.
func (b *thorchainBridge) GetBlockHeightUncached() (int64, error) {
	latestBlocks, err := b.getLastBlockUncached(common.EmptyChain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetThorchainHeight: %w", err)
	}
	for _, item := range latestBlocks {
		return item.Thorchain, nil
	}
	return 0, fmt.Errorf("failed to GetThorchainHeight")
}

// getLastBlock calls the /lastblock/{chain} endpoint and Unmarshal's into the QueryResLastBlockHeights type
func (b *thorchainBridge) getLastBlock(chain common.Chain) ([]openapi.LastBlock, error) {
	return b.getLastBlockWithPath(chain, b.getWithPath)
}

func (b *thorchainBridge) getLastBlockUncached(chain common.Chain) ([]openapi.LastBlock, error) {
	return b.getLastBlockWithPath(chain, b.getWithPathUncached)
}

func (b *thorchainBridge) getLastBlockWithPath(chain common.Chain, get func(string) ([]byte, int, error)) ([]openapi.LastBlock, error) {
	path := LastBlockEndpoint
	if !chain.IsEmpty() {
		path = fmt.Sprintf("%s/%s", path, chain.String())
	}
	buf, _, err := get(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get lastblock: %w", err)
	}
	var lastBlock []openapi.LastBlock
	if err = json.Unmarshal(buf, &lastBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal last block: %w", err)
	}
	return lastBlock, nil
}
