package types

import (
	"errors"
	"slices"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ObservedTxVoters a list of observed tx voter
type ObservedTxVoters []ObservedTxVoter

// NewObservedTxVoter create a new instance of ObservedTxVoter
func NewObservedTxVoter(txID common.TxID, txs []common.ObservedTx) ObservedTxVoter {
	observedTxVoter := ObservedTxVoter{
		TxID: txID,
		Txs:  txs,
	}
	return observedTxVoter
}

// Valid check whether the tx is valid , if it is not , then an error will be returned
func (m *ObservedTxVoter) Valid() error {
	if m.TxID.IsEmpty() {
		return errors.New("cannot have an empty tx id")
	}

	// check all other normal tx
	for _, in := range m.Txs {
		if err := in.Valid(); err != nil {
			return err
		}
	}

	return nil
}

// Key is to get the txid
func (m *ObservedTxVoter) Key() common.TxID {
	return m.TxID
}

// String implement fmt.Stringer
func (m *ObservedTxVoter) String() string {
	return m.TxID.String()
}

// matchActionItem is to check the given outboundTx again the list of actions , return true of the outboundTx matched any of the actions
func (m *ObservedTxVoter) matchActionItem(outboundTx common.Tx) bool {
	for _, toi := range m.Actions {
		// note: Coins.Contains will match amount as well
		matchCoin := outboundTx.Coins.Contains(toi.Coin)
		if !matchCoin && toi.Coin.Asset.Equals(toi.Chain.GetGasAsset()) {
			asset := toi.Chain.GetGasAsset()
			intendToSpend := toi.Coin.Amount.Add(toi.MaxGas.ToCoins().GetCoin(asset).Amount)
			actualSpend := outboundTx.Coins.GetCoin(asset).Amount.Add(outboundTx.Gas.ToCoins().GetCoin(asset).Amount)
			if intendToSpend.Equal(actualSpend) {
				matchCoin = true
			}

		}
		// Use GetMemo() to compare memos for memoless outbounds where
		// toi.Memo is empty but toi.OriginalMemo contains the actual memo
		if strings.EqualFold(toi.GetMemo(), outboundTx.Memo) &&
			common.EquivalentAddresses(toi.Chain, toi.ToAddress, outboundTx.ToAddress) &&
			toi.Chain.Equals(outboundTx.Chain) &&
			matchCoin {
			return true
		}
	}
	return false
}

// AddOutTx trying to add the outbound tx into OutTxs ,
// return value false indicate the given outbound tx doesn't match any of the
// actions items , node account should be slashed for a malicious tx
// true indicated the outbound tx matched an action item , and it has been
// added into internal OutTxs
func (m *ObservedTxVoter) AddOutTx(in common.Tx) bool {
	if !m.matchActionItem(in) {
		// no action item match the outbound tx
		return false
	}
	// As an Asset->RUNE affiliate fee could also be RUNE,
	// allow multiple OutTxs with blank TxIDs.
	// AddOutTxs is still expected to only be called once for each.
	if !in.ID.Equals(common.BlankTxID) {
		for _, t := range m.OutTxs {
			if in.ID.Equals(t.ID) {
				return true
			}
		}
	}
	m.OutTxs = append(m.OutTxs, in)
	for i := range m.Txs {
		m.Txs[i].SetDone(in.ID, len(m.Actions))
	}

	if !m.Tx.IsEmpty() {
		m.Tx.SetDone(in.ID, len(m.Actions))
	}

	return true
}

// IsDone check whether THORChain finished process the tx, all outbound tx had
// been sent and observed
func (m *ObservedTxVoter) IsDone() bool {
	return len(m.Actions) <= len(m.OutTxs)
}

// Add is trying to add the given observed tx into the voter , if the signer
// already sign , they will not add twice , it simply return false
func (m *ObservedTxVoter) Add(observedTx common.ObservedTx, signer cosmos.AccAddress) bool {
	// check if this signer has already signed, no take backs allowed
	votedIdx := -1
	for idx, transaction := range m.Txs {
		if !transaction.Equals(observedTx) {
			continue
		}
		votedIdx = idx
		// check whether the signer is already in the list
		if transaction.HasSigned(signer) {
			return false
		}
	}
	if votedIdx != -1 {
		return m.Txs[votedIdx].Sign(signer)
	}
	observedTx.Signers = []string{signer.String()}
	m.Txs = append(m.Txs, observedTx)
	return true
}

// HasConsensus is to check whether the tx with finalise = false in this ObservedTxVoter reach consensus
// if ObservedTxVoter HasFinalised , then this function will return true as well
func (m *ObservedTxVoter) HasConsensus(nodeAccounts NodeAccounts) bool {
	consensusTx := m.GetTx(nodeAccounts)
	return !consensusTx.IsEmpty()
}

// HasFinalised is to check whether the tx with finalise = true  reach super majority
func (m *ObservedTxVoter) HasFinalised(nodeAccounts NodeAccounts) bool {
	finalTx := m.GetTx(nodeAccounts)
	if finalTx.IsEmpty() {
		return false
	}
	return finalTx.IsFinal()
}

// GetTx return the tx that has super majority
func (m *ObservedTxVoter) GetTx(nodeAccounts NodeAccounts) *common.ObservedTx {
	if !m.Tx.IsEmpty() && m.Tx.IsFinal() {
		return &m.Tx
	}
	// both passes share one index of the active node addresses
	var idx activeNodeIndex
	finalTx := m.getConsensusTx(nodeAccounts, true, &idx)
	if !finalTx.IsEmpty() {
		return &finalTx
	}
	discoverTx := m.getConsensusTx(nodeAccounts, false, &idx)
	if !discoverTx.IsEmpty() {
		return &discoverTx
	}
	return &m.Tx
}

func sameConsensusObservation(lhs, rhs common.ObservedTx) bool {
	return lhs.EqualsConsensus(rhs)
}

// activeNodeIndex gives each distinct active node address a slot in [0, n),
// so getConsensusTx can count distinct active signers with a []bool instead
// of a map of strings. It is built on first use and lives only for one GetTx.
type activeNodeIndex struct {
	built bool
	slots map[string]int
	n     int
	voted []bool // scratch, one flag per slot
}

// signerSlot is the slot of the address signer decodes to, exactly as
// GetSigners decodes it, or -1 if GetSigners skips signer or the address is
// not active. Unlike GetSigners, it allocates nothing for a memoized signer.
func (idx *activeNodeIndex) signerSlot(signer string) int {
	if addr, ok := common.SignerAddressString(signer); ok {
		if slot, active := idx.slots[addr]; active {
			return slot
		}
	}
	return -1
}

func (idx *activeNodeIndex) build(accounts NodeAccounts) {
	idx.built = true
	size, count := 0, 0
	for i := range accounts {
		if accounts[i].Status == NodeStatus_Active {
			size += len(accounts[i].NodeAddress)
			count++
		}
	}
	// one copy of all active addresses backs every key, instead of one string
	// allocation per address
	var all strings.Builder
	all.Grow(size)
	for i := range accounts {
		if accounts[i].Status == NodeStatus_Active {
			all.Write(accounts[i].NodeAddress)
		}
	}
	keys := all.String()
	idx.slots = make(map[string]int, count)
	off := 0
	for i := range accounts {
		if accounts[i].Status != NodeStatus_Active {
			continue
		}
		key := keys[off : off+len(accounts[i].NodeAddress)]
		off += len(accounts[i].NodeAddress)
		// a repeated address keeps its first slot, so it counts once
		if _, exist := idx.slots[key]; !exist {
			idx.slots[key] = idx.n
			idx.n++
		}
	}
	idx.voted = make([]bool, idx.n)
}

// countActiveSigners is the number of signers that decode, as GetSigners
// decodes them, to the address of an active node in accounts. A repeated
// signer counts each time, as when IsNodeKeys is called per GetSigners entry,
// but each signer is a map lookup instead of a decode and a linear scan.
func countActiveSigners(signers []string, accounts NodeAccounts) int {
	var idx activeNodeIndex
	count := 0
	for _, signer := range signers {
		if !idx.built {
			idx.build(accounts)
		}
		if idx.signerSlot(signer) >= 0 {
			count++
		}
	}
	return count
}

// signersInclude reports whether any of signers decodes, as GetSigners decodes
// it, to signer. Decoded signers are non-empty, so AccAddress.Equals on them is
// byte equality.
func signersInclude(signers []string, signer cosmos.AccAddress) bool {
	for _, a := range signers {
		if addr, ok := common.SignerAddressString(a); ok && addr == string(signer) {
			return true
		}
	}
	return false
}

func (m *ObservedTxVoter) getConsensusTx(accounts NodeAccounts, final bool, idx *activeNodeIndex) common.ObservedTx {
	// accounts.IsNodeKeys(signer) for every signer is a linear scan; index the
	// active node addresses once. Signers decode to non-empty addresses, for
	// which AccAddress.Equals is byte equality.
	for _, txFinal := range m.Txs {
		if txFinal.IsFinal() != final {
			continue
		}
		if !idx.built {
			idx.build(accounts)
		}
		clear(idx.voted)
		voters := 0
		for i := range m.Txs {
			txIn := &m.Txs[i]
			if txIn.IsFinal() != final {
				continue
			}
			if !sameConsensusObservation(txFinal, *txIn) {
				continue
			}
			for _, signer := range txIn.Signers {
				slot := idx.signerSlot(signer)
				if slot >= 0 && !idx.voted[slot] {
					idx.voted[slot] = true
					voters++
				}
			}
		}
		if HasSuperMajority(voters, len(accounts)) {
			return txFinal
		}
	}
	return common.ObservedTx{}
}

// SetReverted set all the tx status to `Reverted` , only when a relevant errata tx had been processed
func (m *ObservedTxVoter) SetReverted() {
	m.setStatus(common.Status_reverted)
	m.Reverted = true
}

func (m *ObservedTxVoter) setStatus(toStatus common.Status) {
	for _, item := range m.Txs {
		item.Status = toStatus
	}
	if !m.Tx.IsEmpty() {
		m.Tx.Status = toStatus
	}
}

// SetDone set all the tx status to `done`
// usually the status will be set to done once the outbound tx get observed and processed
// there are some situation , it doesn't have outbound , those will need to set manually
func (m *ObservedTxVoter) SetDone() {
	m.setStatus(common.Status_done)
}

// Get consensus signers for slash point decrementation
func (m *ObservedTxVoter) GetConsensusSigners() []cosmos.AccAddress {
	if m.Tx.IsEmpty() {
		return nil
	}

	final := m.Tx.IsFinal()
	signersMap := make(map[string]bool)
	var signers []cosmos.AccAddress
	for _, tx := range m.Txs {
		// Only include signers for Txs matching the Tx's finality.
		if tx.IsFinal() != final {
			continue
		}
		if !sameConsensusObservation(tx, m.Tx) {
			continue
		}

		for _, signer := range tx.GetSigners() {
			// Use a map to ensure only a single record of each signer.
			// However, do not iterate over the map to get the signers slice,
			// as the slice order would vary between nodes.
			if !signersMap[signer.String()] {
				signers = append(signers, signer)
				signersMap[signer.String()] = true
			}
		}
	}

	return signers
}

// GetObserveSigners returns every node that observed the consensus tx while
// IGNORING gas. It is the gas-agnostic sibling of GetConsensusSigners, used for
// observe-point refunds and the lack-of-observation penalty so an honest
// observer whose only difference is the gas value is credited as a signer.
// The motivating case is a signer's pre-broadcast gas-limit "instant"
// observation of an outbound, which loses gas-sensitive consensus to the
// scanner's later gas-used observation and would otherwise never be refunded
// its observe slash point. The authoritative consensus tx/gas selection
// (GetTx/getConsensusTx) stays gas-sensitive, so recorded gas and vault
// accounting are unaffected. For pre-mining XMR outbounds, use
// GetOutboundObserveSigners to also allow the unknown mined-height sentinel.
func (m *ObservedTxVoter) GetObserveSigners() []cosmos.AccAddress {
	return m.getObserveSigners(m.Tx.EqualsConsensusIgnoreGas)
}

// GetInboundObserveSigners also credits legacy XMR observations that lack the
// mined-height field. Only slash accounting uses this compatibility matching.
func (m *ObservedTxVoter) GetInboundObserveSigners() []cosmos.AccAddress {
	return m.getObserveSigners(m.Tx.EqualsInboundObservationCredit)
}

// GetOutboundObserveSigners also credits legacy non-final and final pre-mining
// XMR observations of the same outbound. Only slash accounting uses this
// compatibility matching.
func (m *ObservedTxVoter) GetOutboundObserveSigners() []cosmos.AccAddress {
	return m.getObserveSigners(m.Tx.EqualsOutboundObservationCredit)
}

func (m *ObservedTxVoter) getObserveSigners(matches func(common.ObservedTx) bool) []cosmos.AccAddress {
	if m.Tx.IsEmpty() {
		return nil
	}

	final := m.Tx.IsFinal()
	signersMap := make(map[string]bool)
	var signers []cosmos.AccAddress
	for _, tx := range m.Txs {
		// Only include signers for Txs matching the Tx's finality.
		if tx.IsFinal() != final {
			continue
		}
		if !matches(tx) {
			continue
		}

		for _, signer := range tx.GetSigners() {
			// Use a map to ensure only a single record of each signer.
			// However, do not iterate over the map to get the signers slice,
			// as the slice order would vary between nodes.
			if !signersMap[signer.String()] {
				signers = append(signers, signer)
				signersMap[signer.String()] = true
			}
		}
	}

	return signers
}

// RecordObserveSigners marks the given gas-agnostic observer set as credited on
// the consensus Tx by appending any signer not already present to Tx.Signers.
// The ObservationDelayFlexibility ("late signer") refund branches key their
// "already refunded" check off Tx.HasSigned, but the consensus Tx.Signers only
// holds the gas-sensitive signer set. Without recording the wider GetObserveSigners
// set here, a signer credited at consensus via its gas-limit observation (and so
// absent from the gas-used consensus Tx.Signers) would be refunded a second time
// when its own gas-used observation lands after consensus, removing unrelated
// pre-existing slash points. Signer order follows the deterministic
// GetObserveSigners output, so the appended Tx.Signers stays identical across nodes.
func (m *ObservedTxVoter) RecordObserveSigners(signers []cosmos.AccAddress) {
	// Clone-on-write before appending. m.Tx is a shallow struct copy of a Txs
	// entry (see getConsensusTx), so m.Tx.Signers shares its backing array with
	// the originating m.Txs[i].Signers. An in-place append here would write into
	// that shared array; a later append to the sibling slice (e.g. another
	// observation via Add) could then silently overwrite the consensus Tx's
	// freshly-credited signer, and vice versa. Cloning gives m.Tx.Signers its own
	// backing array so the two evolve independently.
	m.Tx.Signers = slices.Clone(m.Tx.Signers)
	for _, signer := range signers {
		if !m.Tx.HasSigned(signer) {
			m.Tx.Signers = append(m.Tx.Signers, signer.String())
		}
	}
}

// equalsObservationRefundClass reports whether two observations share the same
// one-refund allowance. It deliberately ignores gas, block heights, and XMR
// spent-output refs, while still requiring the same underlying transaction,
// observed vault, and XMR tx key.
//
// Do not replace this with EqualsConsensusIgnoreGas. For XMR, an empty
// spent-output-ref list acts as a wildcard for consensus: it matches every
// non-empty list even though two different non-empty lists do not match each
// other. That non-transitive "joker" behavior is required for consensus when an
// honest observer lacks optional refs, but using it for refund accounting would
// let a signer submit unlimited ref variants while keeping the match count at 1.
func equalsObservationRefundClass(a, b common.ObservedTx) bool {
	return a.ObservedPubKey.Equals(b.ObservedPubKey) &&
		a.Tx.EqualsExIgnoreGas(b.Tx) &&
		a.HasEquivalentXMRTxKey(b)
}

// equalsRefundableObservation reports whether two observations are close enough
// for the special immediate redundant-observation refund. Unlike XMR consensus
// matching, it requires canonically identical spent-output refs: the empty-ref
// wildcard is non-transitive and could otherwise bridge conflicting ref sets.
func equalsRefundableObservation(a, b common.ObservedTx) bool {
	return equalsObservationRefundClass(a, b) &&
		common.SpentOutputRefsEqual(a.SpentOutputRefs, b.SpentOutputRefs)
}

// ShouldRefundRedundantInboundObservation preserves the one-variant allowance
// for legacy XMR observations, but never refunds a conflicting concrete height.
func (m *ObservedTxVoter) ShouldRefundRedundantInboundObservation(tx common.ObservedTx, signer cosmos.AccAddress) bool {
	return m.shouldRefundRedundantObservation(tx, signer, common.ObservedTx.EqualsInboundObservationCredit)
}

// ShouldRefundRedundantOutboundObservation uses the same transitive allowance
// and exact-ref check, but also requires matching outbound credit identity. A
// conflicting concrete mining location must keep its original observe point
// when a later matching observation receives the late-signer refund.
func (m *ObservedTxVoter) ShouldRefundRedundantOutboundObservation(tx common.ObservedTx, signer cosmos.AccAddress) bool {
	return m.shouldRefundRedundantObservation(tx, signer, common.ObservedTx.EqualsOutboundObservationCredit)
}

// shouldRefundRedundantObservation reports whether this is the signer's first
// redundant observation in the refund class and a prior observation has the
// same canonical XMR spent refs. A committee member's pre-broadcast "instant"
// observation and its later scanner observation can differ only in gas or block
// height and land as separate voter entries. Call this before Add so the
// submitted observation is not counted as its own prior match.
//
// The redundant-observation refund is eligible only when the prior count is 1
// and that prior observation has the same canonical XMR spent refs — the signer's
// single original observation in the honest "observed it, lost the gas/height
// race, re-observed once" case. A count of 2+ means the signer already submitted
// a redundant variant, so any further variant is self-inflicted spam and keeps
// its observe slash point; otherwise a node could file unlimited gas/height
// variants at zero cost, defeating the per-observation charge.
//
// Two costs are knowingly ACCEPTED rather than engineered away, both bounded:
//   - Signing-party bias: a committee member files two legitimate entries per
//     outbound — the pre-broadcast "instant" observation plus the scanner
//     observation — so a gas-correction reorg makes its corrected re-observation
//     variant #3 (count 2) and it keeps one observe point, while a non-committee
//     node (single entry) is still refunded. This is a bounded ~1-point-per-
//     gas-correction-reorg bias against the signing party. The alternatives are
//     worse: refunding count>=2 re-opens the unlimited-variant spam hole, and
//     gating the refund on WHY an observation differs makes a consensus-critical
//     count depend on the reason — a determinism hazard.
//   - Bogus-class floor: the first redundant variant of an ignore-gas class that
//     never reaches consensus is still refunded, because pre-consensus a bogus
//     instant/scanner pair is indistinguishable from an honest one. So spam of N
//     distinct bogus classes costs N-1 points, not N — inherent to the base
//     feature and bounded at one free variant per class.
func (m *ObservedTxVoter) shouldRefundRedundantObservation(tx common.ObservedTx, signer cosmos.AccAddress, matches func(common.ObservedTx, common.ObservedTx) bool) bool {
	// A legacy zero-height variant must not bridge two conflicting locations
	// once the consensus transaction has a concrete mined height.
	if m.Tx.Tx.Chain.Equals(common.XMRChain) && tx.Tx.Chain.Equals(common.XMRChain) &&
		m.Tx.IsFinal() == tx.IsFinal() && m.Tx.XmrMinedHeight > 0 && tx.XmrMinedHeight > 0 &&
		!m.Tx.HasEquivalentCanonicalMinedHeight(tx) {
		return false
	}
	count := 0
	hasRefundableMatch := false
	for _, existing := range m.Txs {
		// Final and non-final observations receive separate normal consensus
		// credits, so they must also have separate redundant-refund allowances.
		if existing.IsFinal() != tx.IsFinal() || !existing.HasSigned(signer) {
			continue
		}
		if !equalsObservationRefundClass(existing, tx) {
			continue
		}
		count++
		if equalsRefundableObservation(existing, tx) &&
			matches(existing, tx) {
			hasRefundableMatch = true
		}
	}
	return count == 1 && hasRefundableMatch
}
