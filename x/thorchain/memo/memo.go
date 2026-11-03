package thorchain

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	"github.com/blang/semver"
)

// TXTYPE:STATE1:STATE2:STATE3:FINALMEMO

type TxType uint8

const (
	TxUnknown TxType = iota
	TxAdd
	TxWithdraw
	TxSwap
	TxLimitSwap
	TxModifyLimitSwap
	TxOutbound
	TxDonate
	TxBond
	TxUnbond
	TxLeave
	TxReserve
	TxRefund
	TxMigrate
	TxRagnarok
	TxNoOp
	TxConsolidate
	TxTHORName
	TxTradeAccountDeposit
	TxTradeAccountWithdrawal
	TxSecuredAssetDeposit
	TxSecuredAssetWithdraw
	TxStableReserveDeposit
	TxStableReserveWithdraw
	TxRunePoolDeposit
	TxRunePoolWithdraw
	TxSwitch
	TxReferenceWriteMemo
	TxReferenceReadMemo
	TxTCYClaim
	TxTCYStake
	TxTCYUnstake
	TxMaint
	TxRebond
	TxOperatorRotate
	TxBondYield
)

var stringToTxTypeMap = map[string]TxType{
	"add":         TxAdd,
	"+":           TxAdd,
	"withdraw":    TxWithdraw,
	"wd":          TxWithdraw,
	"-":           TxWithdraw,
	"swap":        TxSwap,
	"s":           TxSwap,
	"=":           TxSwap,
	"=<":          TxLimitSwap,
	"m=<":         TxModifyLimitSwap,
	"out":         TxOutbound,
	"donate":      TxDonate,
	"d":           TxDonate,
	"bond":        TxBond,
	"unbond":      TxUnbond,
	"rebond":      TxRebond,
	"leave":       TxLeave,
	"reserve":     TxReserve,
	"refund":      TxRefund,
	"migrate":     TxMigrate,
	"ragnarok":    TxRagnarok,
	"noop":        TxNoOp,
	"consolidate": TxConsolidate,
	"name":        TxTHORName,
	"n":           TxTHORName,
	"~":           TxTHORName,
	"reference":   TxReferenceWriteMemo, // etup reference memo
	"r":           TxReferenceReadMemo,  // use reference memo
	"trade+":      TxTradeAccountDeposit,
	"trade-":      TxTradeAccountWithdrawal,
	"secure+":     TxSecuredAssetDeposit,
	"secure-":     TxSecuredAssetWithdraw,
	"stable+":     TxStableReserveDeposit,
	"stable-":     TxStableReserveWithdraw,
	"pool+":       TxRunePoolDeposit,
	"pool-":       TxRunePoolWithdraw,
	"switch":      TxSwitch,
	"tcy":         TxTCYClaim,
	"tcy+":        TxTCYStake,
	"tcy-":        TxTCYUnstake,
	"maint":       TxMaint,
	"operator":    TxOperatorRotate,
	"bondyield":   TxBondYield,
}

var txToStringMap = map[TxType]string{
	TxAdd:                    "add",
	TxWithdraw:               "withdraw",
	TxSwap:                   "swap",
	TxLimitSwap:              "=<",
	TxModifyLimitSwap:        "m=<",
	TxOutbound:               "out",
	TxRefund:                 "refund",
	TxDonate:                 "donate",
	TxBond:                   "bond",
	TxUnbond:                 "unbond",
	TxRebond:                 "rebond",
	TxLeave:                  "leave",
	TxReserve:                "reserve",
	TxMigrate:                "migrate",
	TxRagnarok:               "ragnarok",
	TxNoOp:                   "noop",
	TxConsolidate:            "consolidate",
	TxTHORName:               "thorname",
	TxReferenceWriteMemo:     "reference",
	TxReferenceReadMemo:      "r",
	TxTradeAccountDeposit:    "trade+",
	TxTradeAccountWithdrawal: "trade-",
	TxSecuredAssetDeposit:    "secure+",
	TxSecuredAssetWithdraw:   "secure-",
	TxStableReserveDeposit:   "stable+",
	TxStableReserveWithdraw:  "stable-",
	TxSwitch:                 "switch",
	TxTCYClaim:               "tcy",
	TxTCYStake:               "tcy+",
	TxTCYUnstake:             "tcy-",
	TxMaint:                  "maint",
	TxBondYield:              "bondyield",
}

// converts a string into a txType
func StringToTxType(s string) (TxType, error) {
	// THORNode can support Abbreviated MEMOs , usually it is only one character
	sl := strings.ToLower(s)
	if t, ok := stringToTxTypeMap[sl]; ok {
		return t, nil
	}

	return TxUnknown, fmt.Errorf("invalid tx type: %s", s)
}

func (tx TxType) IsInbound() bool {
	switch tx {
	case TxAdd,
		TxWithdraw,
		TxTradeAccountDeposit,
		TxTradeAccountWithdrawal,
		TxRunePoolDeposit,
		TxRunePoolWithdraw,
		TxSecuredAssetDeposit,
		TxSecuredAssetWithdraw,
		TxStableReserveDeposit,
		TxStableReserveWithdraw,
		TxReferenceWriteMemo,
		TxReferenceReadMemo,
		TxSwap,
		TxLimitSwap,
		TxModifyLimitSwap,
		TxDonate,
		TxBond,
		TxUnbond,
		TxRebond,
		TxLeave,
		TxMaint,
		TxReserve,
		TxNoOp,
		TxTHORName,
		TxSwitch,
		TxTCYClaim,
		TxTCYStake,
		TxTCYUnstake,
		TxOperatorRotate,
		TxBondYield:
		return true
	default:
		return false
	}
}

func (tx TxType) IsOutbound() bool {
	switch tx {
	case TxOutbound, TxRefund, TxRagnarok:
		return true
	default:
		return false
	}
}

func (tx TxType) IsOutboundMemoless() bool {
	switch tx {
	case TxOutbound, TxRefund:
		return true
	default:
		return false
	}
}

func (tx TxType) IsInternal() bool {
	switch tx {
	case TxMigrate, TxConsolidate:
		return true
	default:
		return false
	}
}

// HasOutbound whether the txtype might trigger outbound tx
func (tx TxType) HasOutbound() bool {
	switch tx {
	case TxAdd,
		TxBond,
		TxTradeAccountDeposit,
		TxSecuredAssetDeposit,
		TxStableReserveDeposit,
		TxRunePoolDeposit,
		TxDonate,
		TxReserve,
		TxMigrate,
		TxMaint,
		TxBondYield,
		TxRagnarok:
		return false
	default:
		return true
	}
}

func (tx TxType) IsEmpty() bool {
	return tx == TxUnknown
}

// Check if two txTypes are the same
func (tx TxType) Equals(tx2 TxType) bool {
	return tx == tx2
}

// Converts a txType into a string
func (tx TxType) String() string {
	return txToStringMap[tx]
}

type Memo interface {
	IsType(tx TxType) bool
	GetType() TxType
	IsEmpty() bool
	IsInbound() bool
	IsOutbound() bool
	IsInternal() bool
	String() string
	GetAsset() common.Asset
	GetAmount() cosmos.Uint
	GetDestination() common.Address
	GetSlipLimit() cosmos.Uint
	GetTxID() common.TxID
	GetAccAddress() cosmos.AccAddress
	GetBlockHeight() int64
	GetDexAggregator() string
	GetDexTargetAddress() string
	GetDexTargetLimit() *cosmos.Uint
	GetAffiliateTHORName() *types.THORName
	GetRefundAddress() common.Address
	GetAffiliates() []string
	GetAffiliatesBasisPoints() []cosmos.Uint
	GetAddress() common.Address
}

type MemoBase struct {
	TxType TxType
	Asset  common.Asset
}

var EmptyMemo = MemoBase{TxType: TxUnknown, Asset: common.EmptyAsset}

func (m MemoBase) String() string                          { return "" }
func (m MemoBase) GetType() TxType                         { return m.TxType }
func (m MemoBase) IsType(tx TxType) bool                   { return m.TxType.Equals(tx) }
func (m MemoBase) GetAsset() common.Asset                  { return m.Asset }
func (m MemoBase) GetAmount() cosmos.Uint                  { return cosmos.ZeroUint() }
func (m MemoBase) GetDestination() common.Address          { return "" }
func (m MemoBase) GetSlipLimit() cosmos.Uint               { return cosmos.ZeroUint() }
func (m MemoBase) GetTxID() common.TxID                    { return "" }
func (m MemoBase) GetAccAddress() cosmos.AccAddress        { return cosmos.AccAddress{} }
func (m MemoBase) GetBlockHeight() int64                   { return 0 }
func (m MemoBase) IsOutbound() bool                        { return m.TxType.IsOutbound() }
func (m MemoBase) IsInbound() bool                         { return m.TxType.IsInbound() }
func (m MemoBase) IsInternal() bool                        { return m.TxType.IsInternal() }
func (m MemoBase) IsEmpty() bool                           { return m.TxType.IsEmpty() }
func (m MemoBase) GetDexAggregator() string                { return "" }
func (m MemoBase) GetDexTargetAddress() string             { return "" }
func (m MemoBase) GetDexTargetLimit() *cosmos.Uint         { return nil }
func (m MemoBase) GetAffiliateTHORName() *types.THORName   { return nil }
func (m MemoBase) GetRefundAddress() common.Address        { return common.NoAddress }
func (m MemoBase) GetAffiliates() []string                 { return nil }
func (m MemoBase) GetAffiliatesBasisPoints() []cosmos.Uint { return nil }
func (m MemoBase) GetAddress() common.Address              { return common.NoAddress }

// errMemoPanic marks a recovered parser panic. The formatted error text is
// unchanged, but callers can tell it apart from validation errors that echo
// the memo back.
var errMemoPanic = errors.New("panicked parsing memo")

func ParseMemo(version semver.Version, memo string) (mem Memo, err error) {
	defer func() {
		if r := recover(); r != nil {
			mem = EmptyMemo
			err = fmt.Errorf("%w(%s), err: %s", errMemoPanic, memo, r)
		}
	}()

	parser, err := newParser(cosmos.Context{}, nil, version, memo)
	if err != nil {
		return EmptyMemo, err
	}

	return parser.parse()
}

func ParseMemoWithTHORNames(ctx cosmos.Context, keeper keeper.Keeper, memo string) (mem Memo, err error) {
	defer func() {
		if r := recover(); r != nil {
			mem = EmptyMemo
			err = fmt.Errorf("%w(%s), err: %s", errMemoPanic, memo, r)
		}
	}()

	parser, err := newParser(ctx, keeper, keeper.GetVersion(), memo)
	if err != nil {
		return EmptyMemo, err
	}

	return parser.parse()
}

type memoCacheKey struct{}

type memoCacheEntry struct {
	mem Memo
	err error
}

// ParseMemoCache memoizes memo parsing for a context-scoped execution phase.
// Keeper state and version are invariant during advanced swap queue EndBlock,
// while validate, execution, and settlement otherwise parse the same memo.
//
// Invariants:
//   - Entries are keyed by the raw memo string only. Parsing also depends on
//     THORName lookups and the MultipleAffiliatesMaxCount Mimir, which is safe
//     only because neither is written during advanced swap queue EndBlock. A
//     cache must not span a phase that can change them.
//   - Cached Memo values are shared between callers. Slice fields (e.g. the
//     SwapMemo Affiliates and AffiliatesBasisPoints slices) alias the cached
//     backing arrays, so callers must treat them as read-only.
//   - Parse errors are cached as well: a memo that fails to parse returns the
//     same error for every subsequent lookup within the cache's lifetime.
type ParseMemoCache struct {
	mu      sync.Mutex
	entries map[string]memoCacheEntry
}

func WithMemoCache(ctx cosmos.Context) cosmos.Context {
	return ctx.WithValue(memoCacheKey{}, &ParseMemoCache{entries: make(map[string]memoCacheEntry)})
}

func ParseMemoWithTHORNamesCached(ctx cosmos.Context, k keeper.Keeper, memo string) (Memo, error) {
	cache, _ := ctx.Value(memoCacheKey{}).(*ParseMemoCache)
	if cache == nil {
		return ParseMemoWithTHORNames(ctx, k, memo)
	}

	cache.mu.Lock()
	entry, ok := cache.entries[memo]
	cache.mu.Unlock()
	if ok {
		return entry.mem, entry.err
	}

	mem, err := ParseMemoWithTHORNames(ctx, k, memo)
	cache.mu.Lock()
	cache.entries[memo] = memoCacheEntry{mem: mem, err: err}
	cache.mu.Unlock()
	return mem, err
}

func FetchAddress(ctx cosmos.Context, keeper keeper.Keeper, name string, chain common.Chain) (common.Address, error) {
	// if name is an address, return as is
	addr, err := common.NewAddress(name)
	if err == nil {
		return addr, nil
	}

	parts := strings.SplitN(name, ".", 2)
	if len(parts) > 1 {
		chain, err = common.NewChain(parts[1])
		if err != nil {
			return common.NoAddress, err
		}
	}

	if keeper.THORNameExists(ctx, parts[0]) {
		var thorname types.THORName
		thorname, err = keeper.GetTHORName(ctx, parts[0])
		if err != nil {
			return common.NoAddress, err
		}
		return thorname.GetAlias(chain), nil
	}

	return common.NoAddress, fmt.Errorf("%s is not recognizable", name)
}

func parseTradeTarget(limit string) (cosmos.Uint, error) {
	f, _, err := big.ParseFloat(limit, 10, 0, big.ToZero)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	// Bound the value before f.Int materializes the integer: big.Float keeps
	// the value in scientific form, so "1e300000000" parses cheaply but f.Int
	// would allocate the full 10^exp integer (hundreds of MB) only for
	// NewUintFromBigInt to reject it. Any finite value past 2^256-1 is an invalid
	// Uint regardless. An exponent past int32 (e.g. "1e700000000") overflows to
	// ±Inf, for which f.Int leaves the integer untouched at zero; that would
	// silently become a zero limit, i.e. no price protection, so reject it.
	// Negatives would panic in NewUintFromBigInt; reject them explicitly rather
	// than rely on the memo parser's recover.
	if f.IsInf() || f.Sign() < 0 || f.MantExp(nil) > 256 {
		return cosmos.ZeroUint(), fmt.Errorf("price limit %q is out of range", limit)
	}
	i := new(big.Int)
	f.Int(i) // Note: fractional part will be discarded
	result := cosmos.NewUintFromBigInt(i)
	return result, nil
}
