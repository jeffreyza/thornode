package rpc

import "strconv"

// PIVXBlockIndexStats is one window of pivxd getblockindexstats.
// feeperkb and ttlfee are JSON strings (FormatMoney), in PIV.
// The RPC excludes coinbase and coinstake transactions, so the fee is not
// inflated by the staked principal or masternode reward.
type PIVXBlockIndexStats struct {
	TxCount  int64  `json:"txcount"`
	TxBytes  int64  `json:"txbytes"`
	TTLFee   string `json:"ttlfee"`
	FeePerKB string `json:"feeperkb"`
}

// GetPIVXBlockIndexStats returns aggregated fee stats for [height, height+count).
// pivxd does not implement getblockstats.
func (c *Client) GetPIVXBlockIndexStats(height, count int64) (*PIVXBlockIndexStats, error) {
	var stats struct {
		TxCount  int64      `json:"txcount"`
		TxBytes  int64      `json:"txbytes"`
		TTLFee   flexString `json:"ttlfee"`
		FeePerKB flexString `json:"feeperkb"`
	}
	err := c.Call(&stats, "getblockindexstats", height, count)
	if err != nil {
		return nil, extractBTCError(err)
	}
	return &PIVXBlockIndexStats{
		TxCount:  stats.TxCount,
		TxBytes:  stats.TxBytes,
		TTLFee:   stats.TTLFee.String(),
		FeePerKB: stats.FeePerKB.String(),
	}, nil
}

// flexString accepts either a JSON string or a JSON number. pivxd's
// getblockindexstats prints money with FormatMoney, which is a string, but a
// numeric encoding should not fail the fee report.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		s, err := strconv.Unquote(string(b))
		if err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(b)
	return nil
}

func (f flexString) String() string { return string(f) }
