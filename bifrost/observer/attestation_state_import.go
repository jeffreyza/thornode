package observer

import (
	"bytes"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
)

// Reject missing fields before dispatching a decoded batch. Receiver count and
// signature limits belong to the later bounded-receiver release.
func validateAttestationStateImport(batch common.QuorumState) error {
	check := func(name string, index int, atts []*common.Attestation) error {
		for i, att := range atts {
			if att == nil {
				return fmt.Errorf("%s[%d] attestation[%d] is nil", name, index, i)
			}
		}
		return nil
	}
	for i, item := range batch.QuoTxs {
		if item == nil {
			return fmt.Errorf("transaction[%d] is nil", i)
		}
		if err := check("transaction", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoNetworkFees {
		if item == nil || item.NetworkFee == nil {
			return fmt.Errorf("network fee[%d] or payload is nil", i)
		}
		if err := check("network fee", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoSolvencies {
		if item == nil || item.Solvency == nil {
			return fmt.Errorf("solvency[%d] or payload is nil", i)
		}
		if err := check("solvency", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoErrataTxs {
		if item == nil || item.ErrataTx == nil {
			return fmt.Errorf("errata[%d] or payload is nil", i)
		}
		if err := check("errata", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoPriceFeeds {
		if item == nil || item.PriceFeed == nil {
			return fmt.Errorf("price feed[%d] or payload is nil", i)
		}
		if err := check("price feed", i, item.Attestations); err != nil {
			return err
		}
		for j, rate := range item.PriceFeed.Rates {
			if rate == nil {
				return fmt.Errorf("price feed[%d] rate[%d] is nil", i, j)
			}
		}
	}
	return nil
}

// sameAttestationPayload is called under the retained state's lock before
// counting a state-sync item whose bucket key omits signed fields. Exact valid
// duplicates can still use AddAttestation's verification-free replay path.
func sameAttestationPayload(retained, incoming AttestableItem) bool {
	stored, err := retained.GetSignablePayload()
	if err != nil {
		return false
	}
	presented, err := incoming.GetSignablePayload()
	return err == nil && bytes.Equal(stored, presented)
}
