package types

import (
	"errors"

	"gitlab.com/thorchain/thornode/v3/common"
)

type DbPrefix string

// ChainHeight pairs a chain with a block height.
//
// Keeper methods returning []ChainHeight always return it in ascending chain
// order. That ordering is inherent rather than imposed: each chain is stored
// under its own key suffixed with the chain name, and prefix iteration walks
// keys in ascending byte order, which for uppercase chain names is the same as
// sorting them. Callers therefore never need to sort, and — more importantly —
// cannot forget to.
type ChainHeight struct {
	Chain  common.Chain
	Height int64
}

// ObservationTallyEntry is a voter that reached consensus at ConsensusHeight,
// queued to be scored at DueHeight, once its ObservationDelayFlexibility window
// has closed. Kind and Ref identify the voter in its own store.
type ObservationTallyEntry struct {
	DueHeight       int64
	ConsensusHeight int64
	Kind            string
	Chain           common.Chain
	Ref             []string
}

// ObservationTally is a node's missed and total observation counts on one chain
// since its last churn-in, in tally units.
type ObservationTally struct {
	Chain  common.Chain
	Missed int64
	Total  int64
}

// ErrVaultNotFound an error indicate vault can't be found
var ErrVaultNotFound = errors.New("vault not found")
