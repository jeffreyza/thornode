package utxo

import (
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcjson"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
)

// WaitForBlockAdvance waits until the chain height increases or times out.
func (c *Client) WaitForBlockAdvance(timeout time.Duration) (int64, int64, error) {
	start, err := c.rpc.GetBlockCount()
	if err != nil {
		return 0, 0, err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := c.rpc.GetBlockCount()
		if err == nil && current > start {
			return start, current, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return start, start, fmt.Errorf("timeout waiting for block to advance from %d", start)
}

// GetBlockCount returns the current chain tip height.
func (c *Client) GetBlockCount() (int64, error) {
	return c.rpc.GetBlockCount()
}

// GetMempoolEntry returns mempool details for the given transaction.
func (c *Client) GetMempoolEntry(txID string) (*btcjson.GetMempoolEntryResult, error) {
	return c.rpc.GetMempoolEntry(txID)
}

// GetRawTransactionVerbose returns transaction details, including lock metadata when available.
func (c *Client) GetRawTransactionVerbose(txID string) (*rpc.TxResult, error) {
	return c.rpc.GetRawTransactionVerbose(txID)
}
