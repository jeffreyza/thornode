package common

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blang/semver"
)

var LatestVersion semver.Version = semver.MustParse("999.0.0")

func (o *ObservedTx) IsValid() bool {
	return o != nil
}

// GetSignablePayloadWithInbound returns the data that is signed for verification.
// Covers the full ObservedTx (including FinaliseHeight, BlockHeight, ObservedPubKey,
// Aggregator*) plus the inbound classification bit so a block proposer cannot mutate
// wrapper fields or flip Inbound after attestations are signed. Signing only the inner
// Tx would let a proposer flip FinaliseHeight to BlockHeight (forging IsFinal); leaving
// Inbound unsigned would let a proposer route a legit inbound observation through the
// outbound handler, which slashes the vault for the deposit amount.
//
// KeysignMs is excluded because it is per-observer TSS timing telemetry: only the
// broadcasting node populates it (bifrost/signer/sign.go SetTssKeysignMetric), so
// honest observers would otherwise sign different bytes for the same outbound and
// fail to aggregate quorum.
//
// XmrTxKey and SpentOutputRefs are normalized so that observers who see the same
// XMR metadata in different case produce identical signed bytes. This byte layout
// is consensus-critical: changing it after XMR is live requires an explicit
// version transition so mixed attestors do not sign different bytes.
//
// inbound is appended as a trailing byte so any flip of QuorumTx.Inbound by a malicious
// proposer changes the verified bytes and invalidates every attestation in the quorum.
func (o *ObservedTx) GetSignablePayloadWithInbound(inbound bool) ([]byte, error) {
	return o.getSignablePayloadWithInboundForNetwork(inbound, CurrentChainNetwork)
}

func (o *ObservedTx) getSignablePayloadWithInboundForNetwork(inbound bool, network ChainNetwork) ([]byte, error) {
	canonical := *o
	canonical.KeysignMs = 0
	canonical.XmrTxKey = normalizeString(o.XmrTxKey)
	canonical.SpentOutputRefs = normalizeSpentOutputRefs(o.SpentOutputRefs)
	bz, err := canonical.Marshal()
	if err != nil {
		return nil, err
	}
	// XMR observations can carry outbound ref certificates transported by
	// retiring vault members after churn. Bind every XMR attestation to one
	// THORChain network so all XMR observations share one format and a valid
	// chainnet/test signature cannot be replayed on another network.
	if canonical.Tx.Chain.Equals(XMRChain) {
		prefix := []byte(fmt.Sprintf(
			"domain=thorchain-xmr-observed-tx-v2\nnetwork=%s\npayload_length=%d\n",
			ChainNetworkName(network),
			len(bz),
		))
		bz = append(prefix, bz...)
	}
	var b byte
	if inbound {
		b = 1
	}
	return append(bz, b), nil
}

func normalizeString(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// normalizeSpentOutputRefs returns a sorted copy of refs with the three
// hex-encoded string fields lowercased and trimmed. Two attestors observing
// the same XMR refs in different case must produce identical signed bytes;
// canonicalising here means GetSignablePayloadWithInbound's marshal always sees
// the same form regardless of how bifrost surfaced the input.
func normalizeSpentOutputRefs(refs []OutputRef) []OutputRef {
	sorted := make([]OutputRef, len(refs))
	for i, ref := range refs {
		sorted[i] = OutputRef{
			TxHash:      strings.ToLower(strings.TrimSpace(ref.TxHash)),
			OutputIndex: ref.OutputIndex,
			KeyImage:    strings.ToLower(strings.TrimSpace(ref.KeyImage)),
			SpendTxHash: strings.ToLower(strings.TrimSpace(ref.SpendTxHash)),
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return spentOutputRefIdentity(sorted[i]) < spentOutputRefIdentity(sorted[j])
	})
	return sorted
}

func spentOutputRefIdentity(ref OutputRef) string {
	return fmt.Sprintf(
		"%s:%d:%s:%s",
		strings.ToLower(strings.TrimSpace(ref.TxHash)),
		ref.OutputIndex,
		strings.ToLower(strings.TrimSpace(ref.KeyImage)),
		strings.ToLower(strings.TrimSpace(ref.SpendTxHash)),
	)
}

// SpentOutputRefIdentity returns the canonical identity for an XMR spent-output reference.
func SpentOutputRefIdentity(ref OutputRef) string {
	return spentOutputRefIdentity(ref)
}

// SpentOutputRefsEqual returns whether two XMR spent-output ref sets are
// exactly equivalent after canonical ordering/case normalization.
func SpentOutputRefsEqual(lhs, rhs []OutputRef) bool {
	return spentOutputRefsEqual(lhs, rhs)
}

func spentOutputRefsEqual(lhs, rhs []OutputRef) bool {
	if len(lhs) != len(rhs) {
		return false
	}
	if len(lhs) == 0 {
		return true
	}
	lhs = normalizeSpentOutputRefs(lhs)
	rhs = normalizeSpentOutputRefs(rhs)
	for i := range lhs {
		if spentOutputRefIdentity(lhs[i]) != spentOutputRefIdentity(rhs[i]) {
			return false
		}
	}
	return true
}

func spentOutputRefsIdentity(refs []OutputRef) string {
	if len(refs) == 0 {
		return ""
	}
	var buf bytes.Buffer
	for _, ref := range normalizeSpentOutputRefs(refs) {
		buf.WriteString(spentOutputRefIdentity(ref))
		buf.WriteByte('|')
	}
	return buf.String()
}

func observedTxXMRMetadataUniqueHash(base string, chain Chain, txKey string, refs []OutputRef, minedHeight int64) string {
	if !chain.Equals(XMRChain) {
		return base
	}
	txKey = normalizeString(txKey)
	if txKey == "" && len(refs) == 0 && minedHeight == 0 {
		return base
	}
	return fmt.Sprintf("%X", sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", base, txKey, spentOutputRefsIdentity(refs), minedHeight))))
}

// Equals compares two Attestation and returns true iff they are equal
func (a *Attestation) Equals(att *Attestation) bool {
	if !bytes.Equal(a.Signature, att.Signature) {
		return false
	}
	if !bytes.Equal(a.PubKey, att.PubKey) {
		return false
	}
	return true
}

// Valid - check whether NetworkFee struct represent valid information
func (m *NetworkFee) Valid() error {
	if m.Chain.IsEmpty() {
		return errors.New("chain can't be empty")
	}
	if m.Height <= 0 {
		return fmt.Errorf("height can't be zero or negative: %v", m.Height)
	}
	if m.TransactionSize <= 0 {
		return fmt.Errorf("transaction size can't be zero or negative: %v", m.TransactionSize)
	}
	if m.TransactionRate <= 0 {
		return errors.New("transaction fee rate can't be zero")
	}
	return nil
}

func (nf *NetworkFee) IsValid() bool {
	return nf != nil
}

// GetSignablePayload returns the data that is signed for verification
func (nf *NetworkFee) GetSignablePayload() ([]byte, error) {
	return nf.Marshal()
}

func (nf *NetworkFee) Equals(other *NetworkFee) bool {
	if nf.Chain != other.Chain {
		return false
	}
	if nf.Height != other.Height {
		return false
	}
	if nf.TransactionSize != other.TransactionSize {
		return false
	}
	if nf.TransactionRate != other.TransactionRate {
		return false
	}
	return true
}

func (s *Solvency) Hash() (TxID, error) {
	input := fmt.Sprintf("%s|%s|%s|%d", s.Chain, s.PubKey, s.Coins, s.Height)
	id, err := NewTxID(fmt.Sprintf("%X", sha256.Sum256([]byte(input))))
	if err != nil {
		return "", fmt.Errorf("fail to create msg solvency hash")
	}
	return id, nil
}

func (s *Solvency) IsValid() bool {
	return s != nil
}

// GetSignablePayload returns the data that is signed for verification
func (s *Solvency) GetSignablePayload() ([]byte, error) {
	if s.Id.IsEmpty() {
		id, err := s.Hash()
		if err != nil {
			return nil, fmt.Errorf("fail to create msg solvency hash")
		}
		s.Id = id
	}
	return s.Marshal()
}

func (s *Solvency) Equals(other *Solvency) bool {
	if s.Chain != other.Chain {
		return false
	}
	if s.PubKey != other.PubKey {
		return false
	}
	if s.Height != other.Height {
		return false
	}
	if len(s.Coins) != len(other.Coins) {
		return false
	}
	for i, coin := range s.Coins {
		if !coin.Equals(other.Coins[i]) {
			return false
		}
	}
	return true
}

func (e *ErrataTx) IsValid() bool {
	return e != nil
}

// GetSignablePayload returns the data that is signed for verification
func (e *ErrataTx) GetSignablePayload() ([]byte, error) {
	return e.Marshal()
}

func (e *ErrataTx) Equals(other *ErrataTx) bool {
	if e.Chain != other.Chain {
		return false
	}
	if e.Id != other.Id {
		return false
	}
	return true
}

func (pf *PriceFeed) Valid() error {
	if len(pf.Rates) == 0 {
		return fmt.Errorf("rates is empty")
	}
	if pf.Time <= 0 {
		return fmt.Errorf("invalid timestamp: %d", pf.Time)
	}
	if len(pf.Version) != 8 {
		return fmt.Errorf("invalid version: %x", pf.Version)
	}
	return nil
}

func (pf *PriceFeed) IsValid() bool {
	return pf != nil
}

// GetSignablePayload returns the data that is signed for verification
func (pf *PriceFeed) GetSignablePayload() ([]byte, error) {
	return pf.Marshal()
}

func (pf *PriceFeed) Equals(other *PriceFeed) bool {
	if pf == nil || other == nil {
		return false
	}
	if pf.Time != other.Time {
		return false
	}
	if !bytes.Equal(pf.Version, other.Version) {
		return false
	}
	if len(pf.Rates) != len(other.Rates) {
		return false
	}
	for i, rate := range pf.Rates {
		if rate.Amount != other.Rates[i].Amount {
			return false
		}
		if rate.Decimals != other.Rates[i].Decimals {
			return false
		}
	}
	return true
}

func (qtx *QuorumTx) GetAttestations() []*Attestation {
	return qtx.Attestations
}

func (qtx *QuorumTx) SetAttestations(atts []*Attestation) *QuorumTx {
	qtx.Attestations = atts
	return qtx
}

// RemoveAttestations removes matching attestations from a quorum tx, and returns true if there are no more attestations.
func (qtx *QuorumTx) RemoveAttestations(atts []*Attestation) bool {
	newAtts := removeAttestations(qtx.Attestations, atts)
	qtx.Attestations = newAtts
	return len(newAtts) == 0
}

func (qtx *QuorumTx) Equals(other *QuorumTx) bool {
	if qtx.Inbound != other.Inbound {
		return false
	}
	if qtx.AllowFutureObservation != other.AllowFutureObservation {
		return false
	}
	return qtx.ObsTx.Equals(other.ObsTx)
}

func (qnf *QuorumNetworkFee) GetAttestations() []*Attestation {
	return qnf.Attestations
}

func (qnf *QuorumNetworkFee) SetAttestations(atts []*Attestation) *QuorumNetworkFee {
	qnf.Attestations = atts
	return qnf
}

// RemoveAttestations removes matching attestations from a quorum network fee, and returns true if there are no more attestations.
func (qnf *QuorumNetworkFee) RemoveAttestations(atts []*Attestation) bool {
	newAtts := removeAttestations(qnf.Attestations, atts)
	qnf.Attestations = newAtts
	return len(newAtts) == 0
}

func (qnf *QuorumNetworkFee) Equals(other *QuorumNetworkFee) bool {
	return qnf.NetworkFee.Equals(other.NetworkFee)
}

func (qs *QuorumSolvency) GetAttestations() []*Attestation {
	return qs.Attestations
}

func (qs *QuorumSolvency) SetAttestations(atts []*Attestation) *QuorumSolvency {
	qs.Attestations = atts
	return qs
}

func (qs *QuorumSolvency) RemoveAttestations(atts []*Attestation) bool {
	newAtts := removeAttestations(qs.Attestations, atts)
	qs.Attestations = newAtts
	return len(newAtts) == 0
}

func (qs *QuorumSolvency) Equals(other *QuorumSolvency) bool {
	return qs.Solvency.Equals(other.Solvency)
}

func (qe *QuorumErrataTx) GetAttestations() []*Attestation {
	return qe.Attestations
}

func (qe *QuorumErrataTx) SetAttestations(atts []*Attestation) *QuorumErrataTx {
	qe.Attestations = atts
	return qe
}

func (qe *QuorumErrataTx) RemoveAttestations(atts []*Attestation) bool {
	newAtts := removeAttestations(qe.Attestations, atts)
	qe.Attestations = newAtts
	return len(newAtts) == 0
}

func (qe *QuorumErrataTx) Equals(other *QuorumErrataTx) bool {
	return qe.ErrataTx.Equals(other.ErrataTx)
}

func (qpfb *QuorumPriceFeedBatch) GetAttestations() []*Attestation {
	for _, qpf := range qpfb.QuorumPriceFeeds {
		return qpf.Attestations
	}
	return nil
}

func (qpfb *QuorumPriceFeedBatch) SetAttestations(_ []*Attestation) *QuorumPriceFeedBatch {
	return qpfb
}

// RemoveAttestations removes matching attestations from a quorum network fee, and returns true if there are no more attestations.
func (qpfb *QuorumPriceFeedBatch) RemoveAttestations(_ []*Attestation) bool {
	return true
}

func (qpfb *QuorumPriceFeedBatch) Equals(_ *QuorumPriceFeedBatch) bool {
	return false
}

func removeAttestations(
	existing []*Attestation,
	toRemove []*Attestation,
) []*Attestation {
	newAtts := make([]*Attestation, 0)
	for _, att1 := range existing {
		found := false
		for _, att2 := range toRemove {
			if att1.Equals(att2) {
				found = true
			}
		}
		if !found {
			newAtts = append(newAtts, att1)
		}
	}
	return newAtts
}

// Implement AttestMessage for AttestTx
func (a *AttestTx) GetAttestation() *Attestation {
	return a.Attestation
}

func (a *AttestTx) GetSignablePayload() ([]byte, error) {
	return a.ObsTx.GetSignablePayloadWithInbound(a.Inbound)
}

// Implement AttestMessage for AttestNetworkFee
func (a *AttestNetworkFee) GetAttestation() *Attestation {
	return a.Attestation
}

func (a *AttestNetworkFee) GetSignablePayload() ([]byte, error) {
	return a.NetworkFee.Marshal()
}

// Implement AttestMessage for AttestSolvency
func (a *AttestSolvency) GetAttestation() *Attestation {
	return a.Attestation
}

func (a *AttestSolvency) GetSignablePayload() ([]byte, error) {
	return a.Solvency.Marshal()
}

// Implement AttestMessage for AttestErrataTx
func (a *AttestErrataTx) GetAttestation() *Attestation {
	return a.Attestation
}

func (a *AttestErrataTx) GetSignablePayload() ([]byte, error) {
	return a.ErrataTx.Marshal()
}
