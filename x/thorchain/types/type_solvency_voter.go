package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewSolvencyVoter create a new solvency voter
func NewSolvencyVoter(id common.TxID, chain common.Chain, pubKey common.PubKey, coins common.Coins, height int64) SolvencyVoter {
	return SolvencyVoter{
		Id:     id,
		Chain:  chain,
		PubKey: pubKey,
		Coins:  coins,
		Height: height,
	}
}

func (m *SolvencyVoter) GetSigners() []cosmos.AccAddress {
	addrs := make([]cosmos.AccAddress, 0)
	for _, a := range m.Signers {
		addr, err := cosmos.AccAddressFromBech32(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}

// HasSigned - check if given address has signed
func (m *SolvencyVoter) HasSigned(signer cosmos.AccAddress) bool {
	return signersInclude(m.Signers, signer)
}

// Sign this voter with given signer address
func (m *SolvencyVoter) Sign(signer cosmos.AccAddress) bool {
	if m.HasSigned(signer) {
		return false
	}
	m.Signers = append(m.Signers, signer.String())
	return true
}

// HasConsensus determine if this errata has enough signers
func (m *SolvencyVoter) HasConsensus(nas NodeAccounts) bool {
	count := countActiveSigners(m.Signers, nas)
	return HasMinority(count, len(nas))
}

// Empty check whether TxID or Chain is empty
func (m *SolvencyVoter) Empty() bool {
	return m.Id.IsEmpty() || m.Chain.IsEmpty() || m.Height <= 0 || len(m.Signers) == 0
}

// String implement fmt.Stinger , return a string representation of solvency tx voter
func (m *SolvencyVoter) String() string {
	return m.Id.String()
}
