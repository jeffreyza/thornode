package types

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

var (
	// ErrObservedTxOrphaned identifies the exact observation location removed by
	// an errata handoff. Durable scanners may acknowledge that location, but must
	// allow the same transaction ID to be observed at a new canonical location.
	ErrObservedTxOrphaned = errors.New("observed transaction location is orphaned")

	// ErrObservedTxPermanentlyReverted distinguishes THORChain's permanent
	// TxID-level Reverted barrier from transient Observer rejection. Durable chain
	// scanners must quarantine the exact TxID before acknowledging this rejection.
	ErrObservedTxPermanentlyReverted = errors.New("observed transaction is permanently reverted")

	// ErrObservedTxNotApplicable identifies a durable observation which is valid
	// but this node is not authorized to attest. Scanners may acknowledge it
	// without quarantining the transaction or retaining it for futile replay.
	ErrObservedTxNotApplicable = errors.New("observed transaction is not applicable to this node")
)

type TxIn struct {
	Chain                common.Chain `json:"chain"`
	TxArray              []*TxInItem  `json:"txArray"`
	Filtered             bool         `json:"filtered"`
	MemPool              bool         `json:"mem_pool"` // indicate whether this item is in the mempool or not
	ConfirmationRequired int64        `json:"confirmation_required"`

	// whether this originated from a "instant observation" - e.g. by a member of the signing party
	// immediately after signing, and also has incorrect gas, requiring a re-observation to correct.
	AllowFutureObservation bool `json:"allow_future_observation"`

	// Persisted is an optional local durability acknowledgement. Chain scanners
	// with a restart journal use it to retain replay data until BlockScanner has
	// queued the observations and durably advanced its scan position.
	// Producers must provide a buffered channel with room for one result.
	// Observer does not block if the acknowledgement receiver is unavailable.
	Persisted chan error `json:"-"`
	// FinalizePersistence atomically publishes any chain-scanner replay metadata
	// after Observer persistence succeeds but before BlockScanner advances its
	// scan position. A crash in between only causes a safe duplicate rescan.
	FinalizePersistence func() error `json:"-"`
	// AbortPersistence releases an in-process replay delivery reservation after
	// Observer persistence fails; the synced replay journal remains for retry.
	AbortPersistence func() `json:"-"`
}

type TxInItem struct {
	BlockHeight           int64               `json:"block_height"`
	Tx                    string              `json:"tx"`
	Memo                  string              `json:"memo"`
	Sender                string              `json:"sender"`
	To                    string              `json:"to"` // to address
	Coins                 common.Coins        `json:"coins"`
	Gas                   common.Gas          `json:"gas"`
	ObservedVaultPubKey   common.PubKey       `json:"observed_vault_pub_key"`
	Aggregator            string              `json:"aggregator"`
	AggregatorTarget      string              `json:"aggregator_target"`
	AggregatorTargetLimit *cosmos.Uint        `json:"aggregator_target_limit"`
	SpentOutputRefs       []*common.OutputRef `json:"spent_output_refs,omitempty"`
	TxKey                 string              `json:"tx_key,omitempty"`
	// SolNonce is the post-tx state of the sending vault's durable-nonce account,
	// attached by the SOL scanner to observations of vault txs that created or
	// advanced it. Deliberately NOT part of Equals/EqualsObservedTx: dedup and the
	// commit-event match must not depend on it (a committed consensus tx can carry a
	// different observer's value, and the deck item must still clear).
	SolNonce             *common.SolNonceUpdate `json:"sol_nonce,omitempty"`
	CommittedUnFinalised bool                   `json:"committed_pre_final"`

	// OnObservedTxStored and OnObservedTxRejected are local-only handoff
	// callbacks used by scanners which retain a durable replay record until the
	// observer has durably accepted (or explicitly rejected) the observation.
	// They must never be serialized into observer storage or sent over the wire.
	OnObservedTxStored   func()      `json:"-"`
	OnObservedTxRejected func(error) `json:"-"`
}
type TxInStatus byte

func NewTxInItem(
	blockHeight int64,
	tx string,
	memo string,
	sender string,
	to string,
	coins common.Coins,
	gas common.Gas,
	observedVaultPubKey common.PubKey,
	aggregator string,
	aggregatorTarget string,
	aggregatorTargetLimit *cosmos.Uint,
) *TxInItem {
	return &TxInItem{
		BlockHeight:           blockHeight,
		Tx:                    tx,
		Memo:                  memo,
		Sender:                sender,
		To:                    to,
		Coins:                 coins,
		Gas:                   gas,
		ObservedVaultPubKey:   observedVaultPubKey,
		Aggregator:            aggregator,
		AggregatorTarget:      aggregatorTarget,
		AggregatorTargetLimit: aggregatorTargetLimit,
		CommittedUnFinalised:  false, // stateful parameter used internally in the observer
	}
}

const (
	Processing TxInStatus = iota
	Failed
)

func (t *TxInItem) Equals(other *TxInItem) bool {
	if t.BlockHeight != other.BlockHeight {
		return false
	}
	if t.Tx != other.Tx {
		return false
	}
	if t.Memo != other.Memo {
		return false
	}
	if t.Sender != other.Sender {
		return false
	}
	if t.To != other.To {
		return false
	}
	if !t.Coins.EqualsEx(other.Coins) {
		return false
	}
	if !t.Gas.Equals(other.Gas) {
		return false
	}
	if !t.ObservedVaultPubKey.Equals(other.ObservedVaultPubKey) {
		return false
	}
	if normalizeString(t.TxKey) != normalizeString(other.TxKey) {
		return false
	}
	return true
}

func (t *TxInItem) EqualsObservedTx(other common.ObservedTx) bool {
	// Do not compare block height, as we only keep one deck item for a tx pre/post finalization.
	// The final block height will be ConfirmationCount higher than the unfinalized tx.

	txId, err := common.NewTxID(t.Tx)
	if err != nil {
		return false
	}
	if !txId.Equals(other.Tx.ID) {
		return false
	}
	if t.Memo != other.Tx.Memo {
		return false
	}
	if t.Sender != other.Tx.FromAddress.String() {
		return false
	}
	if t.To != other.Tx.ToAddress.String() {
		return false
	}
	if !t.Coins.EqualsEx(other.Tx.Coins) {
		return false
	}
	if !t.Gas.Equals(other.Tx.Gas) {
		return false
	}
	if !t.ObservedVaultPubKey.Equals(other.ObservedPubKey) {
		return false
	}
	if normalizeString(t.TxKey) != normalizeString(other.XmrTxKey) {
		return false
	}
	return true
}

func (t *TxInItem) Copy() *TxInItem {
	var refs []*common.OutputRef
	if len(t.SpentOutputRefs) > 0 {
		refs = make([]*common.OutputRef, len(t.SpentOutputRefs))
		for i, r := range t.SpentOutputRefs {
			cp := *r
			refs[i] = &cp
		}
	}
	return &TxInItem{
		BlockHeight:           t.BlockHeight,
		Tx:                    t.Tx,
		Memo:                  t.Memo,
		Sender:                t.Sender,
		To:                    t.To,
		Coins:                 t.Coins.Copy(),
		Gas:                   common.Gas(common.Coins(t.Gas).Copy()),
		ObservedVaultPubKey:   t.ObservedVaultPubKey,
		Aggregator:            t.Aggregator,
		AggregatorTarget:      t.AggregatorTarget,
		AggregatorTargetLimit: t.AggregatorTargetLimit,
		SpentOutputRefs:       refs,
		TxKey:                 t.TxKey,
		SolNonce:              copySolNonce(t.SolNonce),
		CommittedUnFinalised:  t.CommittedUnFinalised,
		OnObservedTxStored:    t.OnObservedTxStored,
		OnObservedTxRejected:  t.OnObservedTxRejected,
	}
}

// IsEmpty return true only when every field in TxInItem is empty
func (t *TxInItem) IsEmpty() bool {
	return t.BlockHeight == 0 &&
		t.Tx == "" &&
		t.Memo == "" &&
		t.Sender == "" &&
		t.To == "" &&
		t.Coins.IsEmpty() &&
		t.Gas.IsEmpty() &&
		t.ObservedVaultPubKey.IsEmpty() &&
		t.TxKey == ""
}

func normalizeString(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// CacheHash calculate the has used for signer cache
func (t *TxInItem) CacheHash(chain common.Chain, inboundHash string) string {
	str := fmt.Sprintf("%s|%s|%s|%s|%s", chain, t.To, t.Coins, t.Memo, inboundHash)
	return fmt.Sprintf("%X", sha256.Sum256([]byte(str)))
}

func (t *TxInItem) CacheVault(chain common.Chain) string {
	return InboundCacheKey(t.ObservedVaultPubKey.String(), chain.String())
}

// GetTotalTransactionValue return the total value of the requested asset
func (t TxIn) GetTotalTransactionValue(asset common.Asset, excludeFrom []common.Address) cosmos.Uint {
	total := cosmos.ZeroUint()
	for _, item := range t.TxArray {
		fromAsgard := false
		for _, fromAddress := range excludeFrom {
			if strings.EqualFold(fromAddress.String(), item.Sender) {
				fromAsgard = true
			}
		}
		if fromAsgard {
			continue
		}
		// skip confirmation counting if it is internal tx
		m, err := mem.ParseMemo(common.LatestVersion, item.Memo)
		if err == nil && m.IsInternal() {
			continue
		}
		c := item.Coins.GetCoin(asset)
		if c.IsEmpty() {
			continue
		}
		total = total.Add(c.Amount)
	}
	return total
}

// GetTotalGas return the total gas
func (t TxIn) GetTotalGas() cosmos.Uint {
	total := cosmos.ZeroUint()
	for _, item := range t.TxArray {
		if item.Gas == nil {
			continue
		}
		if err := item.Gas.Valid(); err != nil {
			continue
		}
		total = total.Add(item.Gas[0].Amount)
	}
	return total
}

func InboundCacheKey(vault, chain string) string {
	return fmt.Sprintf("inbound-%s-%s", vault, chain)
}

func copySolNonce(u *common.SolNonceUpdate) *common.SolNonceUpdate {
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}
