package types

import (
	"fmt"
	"strings"

	"cosmossdk.io/math"
)

const MaxDynamicFeeHistory = 30

func NormalizePairAssets(assetA, assetB string) (string, string) {
	if assetA > assetB {
		return assetB, assetA
	}
	return assetA, assetB
}

func (m *DynamicFeeRecord) Key() string {
	return fmt.Sprintf("%s|%s|%s", strings.ToUpper(m.Thorname), m.PairAssetA, m.PairAssetB)
}

func (m *DynamicFeeRecord) AppendHistory(rec DynamicFeeEpochRecord) {
	m.History = append(m.History, rec)
	if len(m.History) > MaxDynamicFeeHistory {
		m.History = m.History[len(m.History)-MaxDynamicFeeHistory:]
	}
}

func (m *DynamicFeeRecord) LatestHistory() (DynamicFeeEpochRecord, bool) {
	if len(m.History) == 0 {
		return DynamicFeeEpochRecord{}, false
	}
	return m.History[len(m.History)-1], true
}

// LastBpsChangeIndex returns the index of the most recent history entry whose
// bps_at_close differs from the entry immediately before it, i.e. the index at
// which the controller last moved the floor. The second return is false when
// the history has fewer than 2 entries or no such transition exists.
func (m *DynamicFeeRecord) LastBpsChangeIndex() (int, bool) {
	if len(m.History) < 2 {
		return 0, false
	}
	for i := len(m.History) - 1; i >= 1; i-- {
		if m.History[i].BpsAtClose != m.History[i-1].BpsAtClose {
			return i, true
		}
	}
	return 0, false
}

func (m *DynamicFeeAccumulator) Key() string {
	return fmt.Sprintf("%s|%s|%s", strings.ToUpper(m.Thorname), m.PairAssetA, m.PairAssetB)
}

func NewDynamicFeeAccumulator(thorname, pairAssetA, pairAssetB string) DynamicFeeAccumulator {
	return DynamicFeeAccumulator{
		Thorname:   thorname,
		PairAssetA: pairAssetA,
		PairAssetB: pairAssetB,
		VolumeTor:  math.ZeroUint(),
		FeesTor:    math.ZeroUint(),
	}
}
