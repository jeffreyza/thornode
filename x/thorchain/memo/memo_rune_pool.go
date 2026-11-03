package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// "pool+"

type RunePoolDepositMemo struct {
	MemoBase
}

func (m RunePoolDepositMemo) String() string {
	return m.string(false)
}

func (m RunePoolDepositMemo) ShortString() string {
	return m.string(true)
}

func (m RunePoolDepositMemo) string(short bool) string {
	return "pool+"
}

func NewRunePoolDepositMemo() RunePoolDepositMemo {
	return RunePoolDepositMemo{
		MemoBase: MemoBase{TxType: TxRunePoolDeposit},
	}
}

func (p *parser) ParseRunePoolDepositMemo() (RunePoolDepositMemo, error) {
	return NewRunePoolDepositMemo(), nil
}

// "pool-:<basis-points>:<affiliate>:<affiliate-basis-points>"

type RunePoolWithdrawMemo struct {
	MemoBase
	BasisPoints          cosmos.Uint
	AffiliateAddress     common.Address
	AffiliateBasisPoints cosmos.Uint
	AffiliateTHORName    *types.THORName
}

func (m RunePoolWithdrawMemo) GetBasisPts() cosmos.Uint              { return m.BasisPoints }
func (m RunePoolWithdrawMemo) GetAffiliateAddress() common.Address   { return m.AffiliateAddress }
func (m RunePoolWithdrawMemo) GetAffiliateBasisPoints() cosmos.Uint  { return m.AffiliateBasisPoints }
func (m RunePoolWithdrawMemo) GetAffiliateTHORName() *types.THORName { return m.AffiliateTHORName }

func (m RunePoolWithdrawMemo) String() string {
	args := []string{TxRunePoolWithdraw.String(), m.BasisPoints.String(), m.AffiliateAddress.String(), m.AffiliateBasisPoints.String()}
	return strings.Join(args, ":")
}

func NewRunePoolWithdrawMemo(basisPoints cosmos.Uint, affAddr common.Address, affBps cosmos.Uint, tn types.THORName) RunePoolWithdrawMemo {
	mem := RunePoolWithdrawMemo{
		MemoBase:             MemoBase{TxType: TxRunePoolWithdraw},
		BasisPoints:          basisPoints,
		AffiliateAddress:     affAddr,
		AffiliateBasisPoints: affBps,
	}
	if !tn.Owner.Empty() {
		mem.AffiliateTHORName = &tn
	}
	return mem
}

func (p *parser) ParseRunePoolWithdrawMemo() (RunePoolWithdrawMemo, error) {
	basisPoints := p.getUint(1, true, cosmos.ZeroInt().Uint64())
	affiliateAddress := p.getAddressWithKeeper(2, false, common.NoAddress, common.THORChain)
	tn := p.getTHORName(2, false, types.NewTHORName("", 0, nil), -1)
	affiliateBasisPoints := p.getUintWithMaxValue(3, false, 0, constants.MaxBasisPts)
	return NewRunePoolWithdrawMemo(basisPoints, affiliateAddress, affiliateBasisPoints, tn), p.Error()
}
