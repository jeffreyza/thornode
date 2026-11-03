package thorchain

import (
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SwapMemo struct {
	MemoBase
	Destination           common.Address
	SlipLimit             cosmos.Uint
	AffiliateAddress      common.Address // TODO: remove on hardfork
	AffiliateBasisPoints  cosmos.Uint    // TODO: remove on hardfork
	DexAggregator         string
	DexTargetAddress      string
	DexTargetLimit        *cosmos.Uint
	SwapType              types.SwapType
	StreamInterval        uint64
	StreamQuantity        uint64
	AffiliateTHORName     *types.THORName // TODO: remove on hardfork
	RefundAddress         common.Address
	Affiliates            []string
	AffiliatesBasisPoints []cosmos.Uint
}

func (m SwapMemo) GetDestination() common.Address          { return m.Destination }
func (m SwapMemo) GetSlipLimit() cosmos.Uint               { return m.SlipLimit }
func (m SwapMemo) GetAffiliateAddress() common.Address     { return m.AffiliateAddress }
func (m SwapMemo) GetAffiliateBasisPoints() cosmos.Uint    { return m.AffiliateBasisPoints }
func (m SwapMemo) GetDexAggregator() string                { return m.DexAggregator }
func (m SwapMemo) GetDexTargetAddress() string             { return m.DexTargetAddress }
func (m SwapMemo) GetDexTargetLimit() *cosmos.Uint         { return m.DexTargetLimit }
func (m SwapMemo) GetSwapType() types.SwapType             { return m.SwapType }
func (m SwapMemo) GetStreamQuantity() uint64               { return m.StreamQuantity }
func (m SwapMemo) GetStreamInterval() uint64               { return m.StreamInterval }
func (m SwapMemo) GetAffiliateTHORName() *types.THORName   { return m.AffiliateTHORName }
func (m SwapMemo) GetRefundAddress() common.Address        { return m.RefundAddress }
func (m SwapMemo) GetAffiliates() []string                 { return m.Affiliates }
func (m SwapMemo) GetAffiliatesBasisPoints() []cosmos.Uint { return m.AffiliatesBasisPoints }

func (m SwapMemo) String() string {
	return m.string(false)
}

func (m SwapMemo) ShortString() string {
	return m.string(true)
}

func (m SwapMemo) string(short bool) string {
	slipLimit := m.SlipLimit.String()
	if m.SlipLimit.IsZero() {
		slipLimit = ""
	}
	if m.StreamInterval > 0 || m.StreamQuantity > 1 {
		slipLimit = fmt.Sprintf("%s/%d/%d", m.SlipLimit.String(), m.StreamInterval, m.StreamQuantity)
	}

	// prefer short notation for generate swap memo
	txType := m.TxType.String()
	if m.TxType == TxSwap {
		txType = "="
	}

	var assetString string
	if short && len(m.Asset.ShortCode()) > 0 {
		assetString = m.Asset.ShortCode()
	} else {
		assetString = m.Asset.String()
	}

	// destination + custom refund addr
	destString := m.Destination.String()
	if !m.RefundAddress.IsEmpty() {
		destString = m.Destination.String() + "/" + m.RefundAddress.String()
	}

	tns := make([]string, len(m.Affiliates))
	copy(tns, m.Affiliates)

	affString := strings.Join(tns, "/")

	affbps := make([]string, len(m.AffiliatesBasisPoints))
	for i, bps := range m.AffiliatesBasisPoints {
		affbps[i] = bps.String()
	}

	affBpsString := strings.Join(affbps, "/")

	args := []string{
		txType,
		assetString,
		destString,
		slipLimit,
		affString,
		affBpsString,
		m.DexAggregator,
		m.DexTargetAddress,
	}

	last := 3
	if !m.SlipLimit.IsZero() || m.StreamInterval > 0 || m.StreamQuantity > 1 {
		last = 4
	}

	if len(m.Affiliates) > 0 {
		last = 6
	}

	if m.DexAggregator != "" {
		last = 8
	}

	if m.DexTargetLimit != nil && !m.DexTargetLimit.IsZero() {
		args = append(args, m.DexTargetLimit.String())
		last = 9
	}

	return strings.Join(args[:last], ":")
}

func NewSwapMemo(asset common.Asset, dest common.Address, slip cosmos.Uint, affAddr common.Address, affPts cosmos.Uint, dexAgg, dexTargetAddress string, dexTargetLimit cosmos.Uint, swapType types.SwapType, quan, interval uint64, tn types.THORName, refundAddress common.Address, affiliates []string, affiliatesFeeBps []cosmos.Uint) SwapMemo {
	swapMemo := SwapMemo{
		MemoBase:              MemoBase{TxType: TxSwap, Asset: asset},
		Destination:           dest,
		SlipLimit:             slip,
		AffiliateAddress:      affAddr,
		AffiliateBasisPoints:  affPts,
		DexAggregator:         dexAgg,
		DexTargetAddress:      dexTargetAddress,
		SwapType:              swapType,
		StreamQuantity:        quan,
		StreamInterval:        interval,
		RefundAddress:         refundAddress,
		Affiliates:            affiliates,
		AffiliatesBasisPoints: affiliatesFeeBps,
	}
	if !dexTargetLimit.IsZero() {
		swapMemo.DexTargetLimit = &dexTargetLimit
	}
	if !tn.Owner.Empty() {
		swapMemo.AffiliateTHORName = &tn
	}
	return swapMemo
}

func (p *parser) ParseSwapMemo() (SwapMemo, error) {
	// TODO confirm and remove this.
	if p.keeper == nil {
		return ParseSwapMemoV1(p.ctx, p.keeper, p.getAsset(1, true, common.EmptyAsset), p.parts)
	}

	var err error
	asset := p.getAsset(1, true, common.EmptyAsset)

	// DESTADDR can be empty , if it is empty , it will swap to the sender address
	destination, refundAddress := p.getAddressAndRefundAddressWithKeeper(2, false, common.NoAddress, asset.Chain)

	// price limit can be empty , when it is empty , there is no price protection
	var slip cosmos.Uint
	var streamInterval, streamQuantity uint64
	if strings.Contains(p.get(3), "/") {
		parts := strings.SplitN(p.get(3), "/", 3)
		for i := range parts {
			if parts[i] == "" {
				parts[i] = "0"
			}
		}
		if len(parts) < 1 {
			return SwapMemo{}, fmt.Errorf("invalid streaming swap format: %s", p.get(3))
		}
		slip, err = parseTradeTarget(parts[0])
		if err != nil {
			return SwapMemo{}, fmt.Errorf("swap price limit:%s is invalid: %s", parts[0], err)
		}
		if len(parts) > 1 {
			streamInterval, err = strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				return SwapMemo{}, fmt.Errorf("failed to parse stream frequency: %s: %s", parts[1], err)
			}
		}
		if len(parts) > 2 {
			streamQuantity, err = strconv.ParseUint(parts[2], 10, 64)
			if err != nil {
				return SwapMemo{}, fmt.Errorf("failed to parse stream quantity: %s: %s", parts[2], err)
			}
		}
	} else {
		slip = p.getUintWithScientificNotation(3, false, 0)
	}

	var swapType types.SwapType
	if strings.EqualFold(p.parts[0], TxLimitSwap.String()) && !slip.IsZero() {
		swapType = types.SwapType_limit
	}

	// Parse multiple affiliate thornames + fee bps
	affiliates := p.getStringArrayBySeparator(4, false, "/")
	affFeeBps := p.getUintArrayBySeparator(5, false, "/")

	// If only one aff bps defined, apply to all affiliates
	if len(affFeeBps) == 1 && len(affiliates) > 1 {
		fee := p.getUintWithMaxValue(5, false, 0, constants.MaxBasisPts)
		affFeeBps = make([]cosmos.Uint, len(affiliates))
		for i := range affFeeBps {
			affFeeBps[i] = fee
		}
	}

	if len(affiliates) != len(affFeeBps) {
		return SwapMemo{}, fmt.Errorf("affiliate thornames and affiliate fee bps count mismatch")
	}

	maxAffiliates := p.keeper.GetConfigInt64(p.ctx, constants.MultipleAffiliatesMaxCount)
	if maxAffiliates < 0 {
		maxAffiliates = 0
	}
	if len(affiliates) > int(maxAffiliates) {
		return SwapMemo{}, fmt.Errorf("maximum allowed affiliates is %d", maxAffiliates)
	}

	totalAffBps := cosmos.ZeroUint()
	for _, bps := range affFeeBps {
		totalAffBps = totalAffBps.Add(bps)
	}
	if totalAffBps.GT(cosmos.NewUint(constants.MaxBasisPts)) {
		return SwapMemo{}, fmt.Errorf("total affiliate fee basis points can't be more than %d", constants.MaxBasisPts)
	}

	// TODO: Remove on hardfork
	// Set a affiliate address (even though it is not used) - to pass validation
	affAddr := common.NoAddress
	if !totalAffBps.IsZero() && len(affiliates) > 0 {
		affAddr = p.getAddressFromString(affiliates[0], common.THORChain, false)
	}

	// Set affiliate thorname when there is only one, this is used in handler_swap to
	// update affiliate collector record (affiliate fee swaps will only have one
	// affiliate)
	tn := p.getTHORName(4, false, types.NewTHORName("", 0, nil), -1)

	dexAgg := p.get(6)
	dexTargetAddress := p.get(7)
	dexTargetLimit := p.getUintWithScientificNotation(8, false, 0)

	return NewSwapMemo(asset, destination, slip, affAddr, totalAffBps, dexAgg, dexTargetAddress, dexTargetLimit, swapType, streamQuantity, streamInterval, tn, refundAddress, affiliates, affFeeBps), p.Error()
}

func ParseSwapMemoV1(ctx cosmos.Context, keeper keeper.Keeper, asset common.Asset, parts []string) (SwapMemo, error) {
	var err error
	var swapType types.SwapType
	if len(parts) < 2 {
		return SwapMemo{}, fmt.Errorf("not enough parameters")
	}
	// DESTADDR can be empty , if it is empty , it will swap to the sender address
	destination := common.NoAddress
	affAddr := common.NoAddress
	affPts := uint64(0)
	if len(parts) > 2 {
		if len(parts[2]) > 0 {
			if keeper == nil {
				destination, err = common.NewAddress(parts[2])
			} else {
				destination, err = FetchAddress(ctx, keeper, parts[2], asset.Chain)
			}
			if err != nil {
				return SwapMemo{}, err
			}
		}
	}
	// price limit can be empty , when it is empty , there is no price protection
	slip := cosmos.ZeroUint()
	if len(parts) > 3 && len(parts[3]) > 0 {
		slip, err = cosmos.ParseUint(parts[3])
		if err != nil {
			return SwapMemo{}, fmt.Errorf("swap price limit:%s is invalid", parts[3])
		}
	}

	if len(parts) > 5 && len(parts[4]) > 0 && len(parts[5]) > 0 {
		if keeper == nil {
			affAddr, err = common.NewAddress(parts[4])
		} else {
			affAddr, err = FetchAddress(ctx, keeper, parts[4], common.THORChain)
		}
		if err != nil {
			return SwapMemo{}, err
		}
		affPts, err = strconv.ParseUint(parts[5], 10, 64)
		if err != nil {
			return SwapMemo{}, err
		}
	}

	return NewSwapMemo(asset, destination, slip, affAddr, cosmos.NewUint(affPts), "", "", cosmos.ZeroUint(), swapType, 0, 0, types.NewTHORName("", 0, nil), "", nil, nil), nil
}
