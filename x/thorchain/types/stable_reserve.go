package types

import "strings"

// Stable Reserve internal control-memo prefixes. These are produced when a
// swap is scheduled and consumed when it settles, so producers and consumers
// must agree exactly.
const (
	StableReserveDeployMemoPrefix      = "stable-reserve-deploy:"
	StableReserveRescueMemoPrefix      = "stable-reserve-rescue:"
	StableReserveWithdrawFeeMemoPrefix = "stable-reserve-withdraw-fee:"

	StableReservePendingDeploySwapKey    = "deploy"
	StableReservePendingRescueSwapPrefix = "rescue/"
)

func StableReservePendingRescueSwapKey(asset string) string {
	return StableReservePendingRescueSwapPrefix + asset
}

func StableReservePendingSwapKeyFromMemo(memo string) (string, bool) {
	if strings.HasPrefix(memo, StableReserveDeployMemoPrefix) {
		return StableReservePendingDeploySwapKey, true
	}
	rescueMemo, ok := strings.CutPrefix(memo, StableReserveRescueMemoPrefix)
	if !ok {
		return "", false
	}
	source, _, ok := strings.Cut(rescueMemo, ":")
	if !ok || source == "" {
		return "", false
	}
	return StableReservePendingRescueSwapKey(source), true
}
