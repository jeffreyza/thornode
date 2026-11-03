package api

import "strings"

// TxInfoResolver resolves the TransactionInfo of transactions within a single
// block. Without batching, every lookup is an individual
// gettransactioninfobyid call. With batching, the first lookup fetches the info
// for the whole block with one gettransactioninfobyblocknum call; any
// transaction missing from that response — or every transaction, if the batch
// call fails — falls back to an individual gettransactioninfobyid call.
//
// The batch response carries the info of every transaction in the block, so it
// only pays off when several transactions in the block need their info; the
// caller decides. Only batch entries recorded at the resolver's height are
// used, so a stale or mismatched entry falls back to the individual lookup
// rather than being returned.
//
// A resolver caches one block's response and is not safe for concurrent use:
// create one per block.
type TxInfoResolver struct {
	api    *TronApi
	height int64
	infos  map[string]TransactionInfo
	// pending is true until the batch call has been attempted; it starts false
	// when batching is disabled.
	pending bool
}

// NewTxInfoResolver returns a TxInfoResolver for the block at the given
// height. With batch false it issues only per-transaction lookups.
func (api *TronApi) NewTxInfoResolver(height int64, batch bool) *TxInfoResolver {
	return &TxInfoResolver{
		api:     api,
		height:  height,
		pending: batch,
	}
}

// Get returns the TransactionInfo for the given transaction id.
func (r *TxInfoResolver) Get(txID string) (TransactionInfo, error) {
	if r.pending {
		r.pending = false
		infos, err := r.api.GetTransactionInfoByBlockNum(r.height)
		if err != nil {
			r.api.logger.Warn().Err(err).
				Int64("height", r.height).
				Msg("failed to get block transaction infos, falling back to per-transaction lookups")
		} else {
			r.infos = make(map[string]TransactionInfo, len(infos))
			for _, info := range infos {
				if info.ID == "" || info.BlockNumber != r.height {
					continue
				}
				r.infos[strings.ToLower(info.ID)] = info
			}
		}
	}

	if info, ok := r.infos[strings.ToLower(txID)]; ok {
		return info, nil
	}

	return r.api.GetTransactionInfo(txID)
}
