package thorchain

import (
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const PreferredAssetSwapMemoPrefix = "THOR-PREFERRED-ASSET"

type swapItem struct {
	index int
	msg   MsgSwap
	// fee is the liquidity fee in RUNE, computed by scoreMsgs.
	// Sort() orders items on this field alone.
	fee cosmos.Uint
}
type swapItems []swapItem

func (item swapItem) GetHash() common.TxID {
	return item.msg.Tx.ID
}

func (items swapItems) HasItem(hash common.TxID) bool {
	for _, item := range items {
		if item.msg.Tx.ID.Equals(hash) {
			return true
		}
	}
	return false
}

// Sort returns a copy of items ordered by liquidity fee (descending), with
// ties broken by TxID (ascending) and then sub-swap index (ascending). The
// explicit tiebreakers matter: arbitrage bots routinely submit swaps that
// land in the same pool on the same block with identical fees, and sub-swaps
// of a single streaming swap share a TxID but differ by index. Relying on
// input order alone would let the keeper's iteration order decide priority.
func (items swapItems) Sort() swapItems {
	sorted := make(swapItems, len(items))
	copy(sorted, items)

	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].fee.Equal(sorted[j].fee) {
			return sorted[i].fee.GT(sorted[j].fee)
		}
		if sorted[i].msg.Tx.ID != sorted[j].msg.Tx.ID {
			return sorted[i].msg.Tx.ID < sorted[j].msg.Tx.ID
		}
		return sorted[i].index < sorted[j].index
	})

	return sorted
}

type tradePair struct {
	source common.Asset
	target common.Asset
}

type tradePairs []tradePair

func genTradePair(s, t common.Asset) tradePair {
	return tradePair{
		source: s,
		target: t,
	}
}

func (pair tradePair) String() string {
	return fmt.Sprintf("%s>%s", pair.source, pair.target)
}

func (pair tradePair) HasRune() bool {
	return pair.source.IsRune() || pair.target.IsRune()
}

func (pair tradePair) Equals(p tradePair) bool {
	return pair.source.Equals(p.source) && pair.target.Equals(p.target)
}

// Append adds a tradePair to the list if it doesn't already exist
func (p tradePairs) Append(pair tradePair) tradePairs {
	// Check if the pair already exists
	for _, existing := range p {
		if existing.Equals(pair) {
			return p
		}
	}
	// If not found, append it
	return append(p, pair)
}

// tradePairSet mirrors the membership of a tradePairs slice, so dedupe on
// append is a map lookup instead of a linear Equals scan. The slice remains
// the source of truth because FetchQueue iterates it in order.
type tradePairSet map[tradePair]struct{}

// tradePairIndex stores pair positions by source and target asset. Position
// slices inherit the deterministic order of getAssetPairs, allowing matching
// lists to be selected in O(P) instead of scanning all O(P^2) pairs for every
// successful swap.
type tradePairIndex struct {
	pairs    tradePairs
	bySource map[common.Asset][]int
	byTarget map[common.Asset][]int
}

func newTradePairIndex(pairs tradePairs) tradePairIndex {
	index := tradePairIndex{
		pairs:    pairs,
		bySource: make(map[common.Asset][]int),
		byTarget: make(map[common.Asset][]int),
	}
	for position, pair := range pairs {
		index.bySource[pair.source] = append(index.bySource[pair.source], position)
		index.byTarget[pair.target] = append(index.byTarget[pair.target], position)
	}
	return index
}

// given a trade pair, find the trading pairs that are the reverse of this
// trade pair. This helps us build a list of trading pairs adv swap queue to check
// for limit swaps later
func (p tradePairs) findMatchingTrades(trade tradePair, pairs tradePairs, seen tradePairSet) tradePairs {
	var comp func(pair tradePair) bool
	switch {
	case trade.source.IsRune():
		comp = func(pair tradePair) bool { return pair.source.Equals(trade.target) }
	case trade.target.IsRune():
		comp = func(pair tradePair) bool { return pair.target.Equals(trade.source) }
	default:
		comp = func(pair tradePair) bool { return pair.source.Equals(trade.target) || pair.target.Equals(trade.source) }
	}
	for _, pair := range pairs {
		if !comp(pair) {
			continue
		}
		if _, ok := seen[pair]; ok {
			continue
		}
		seen[pair] = struct{}{}
		p = append(p, pair)
	}
	return p
}

// findMatchingTradesIndexed is behavior-identical to findMatchingTrades but
// uses the block-scoped position index. If a trade asset is not in canonical
// form, exact map lookup can miss even though Asset.Equals would match; fall
// back to the reference scan in that case.
func (p tradePairs) findMatchingTradesIndexed(trade tradePair, index tradePairIndex, seen tradePairSet) tradePairs {
	appendPosition := func(position int) {
		pair := index.pairs[position]
		if _, ok := seen[pair]; ok {
			return
		}
		seen[pair] = struct{}{}
		p = append(p, pair)
	}

	switch {
	case trade.source.IsRune():
		positions, ok := index.bySource[trade.target]
		if !ok {
			return p.findMatchingTrades(trade, index.pairs, seen)
		}
		for _, position := range positions {
			appendPosition(position)
		}
	case trade.target.IsRune():
		positions, ok := index.byTarget[trade.source]
		if !ok {
			return p.findMatchingTrades(trade, index.pairs, seen)
		}
		for _, position := range positions {
			appendPosition(position)
		}
	default:
		bySource, sourceOK := index.bySource[trade.target]
		byTarget, targetOK := index.byTarget[trade.source]
		if !sourceOK || !targetOK {
			return p.findMatchingTrades(trade, index.pairs, seen)
		}

		// Merge the two ordered position lists and suppress their one possible
		// overlap, preserving the exact global pair order of the scan.
		for sourcePos, targetPos := 0, 0; sourcePos < len(bySource) || targetPos < len(byTarget); {
			switch {
			case targetPos >= len(byTarget) || (sourcePos < len(bySource) && bySource[sourcePos] < byTarget[targetPos]):
				appendPosition(bySource[sourcePos])
				sourcePos++
			case sourcePos >= len(bySource) || byTarget[targetPos] < bySource[sourcePos]:
				appendPosition(byTarget[targetPos])
				targetPos++
			default:
				appendPosition(bySource[sourcePos])
				sourcePos++
				targetPos++
			}
		}
	}
	return p
}
