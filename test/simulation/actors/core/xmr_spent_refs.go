package core

import (
	"fmt"
	rand "math/rand/v2"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simhelpers "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type XMRSpentRefVerifyActor struct {
	Actor
	swapTxID     string
	outboundTxID string
	vaultPubKey  string
	spentBefore  map[string]struct{}

	getSwapStages  func(string) (openapi.TxStagesResponse, error)
	getSwapDetails func(string) (openapi.TxDetailsResponse, error)
}

func NewXMRSpentRefVerifyActor(rng *rand.Rand) *Actor {
	a := &XMRSpentRefVerifyActor{
		Actor:          *NewActor("XMRSpentRefVerify", rng),
		getSwapStages:  thornode.GetTxStages,
		getSwapDetails: thornode.GetTxDetails,
	}
	a.Timeout = swapActorTimeout(common.BTCAsset, common.XMRAsset)

	a.Ops = append(a.Ops, a.verifyVaultHasXMR)
	a.Ops = append(a.Ops, a.swapToXMR)
	a.Ops = append(a.Ops, a.waitForSwapOutbound)
	a.Ops = append(a.Ops, a.waitForSpentRefsOnVault)
	a.Ops = append(a.Ops, a.logVaultBalance)

	return &a.Actor
}

func (a *XMRSpentRefVerifyActor) verifyVaultHasXMR(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaultXMRStates()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vault XMR states")
		return OpResult{Continue: false}
	}

	for _, v := range vaults {
		if v.Status != "ActiveVault" || v.PubKeyFrost == "" {
			continue
		}
		refs, err := thornode.GetXMRSpentOutputRefs(v.PubKeyFrost)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to snapshot spent refs before swap")
			return OpResult{}
		}
		a.captureSpentRefs(v.PubKeyFrost, refs.SpentOutputRefs)
		a.Log().Info().
			Int64("vault_creation_height_xmr", v.VaultCreationHeightXmr).
			Str("pub_key_frost", v.PubKeyFrost).
			Msg("active XMR vault found")
		return OpResult{Continue: true}
	}

	a.Log().Warn().Msg("no active XMR vault found, retrying")
	return OpResult{Continue: false}
}

func (a *XMRSpentRefVerifyActor) swapToXMR(config *OpConfig) OpResult {
	if a.swapTxID != "" {
		return OpResult{Continue: true}
	}
	if result := waitForXMRNetworkFee(a.Log(), config); !result.Continue {
		return result
	}
	if len(config.Users) == 0 {
		a.Log().Error().Msg("no users available")
		return OpResult{Continue: false}
	}
	user := config.Users[0]

	_, ok := user.ChainClients[common.BTCChain]
	if !ok {
		a.Log().Error().Msg("no BTC chain client for swap source")
		return OpResult{Continue: false}
	}

	xmrAddr := user.Address(common.XMRChain)
	amount := cosmos.NewUint(10_000_000)
	memo := fmt.Sprintf("=:XMR.XMR:%s", xmrAddr)

	txid, err := simhelpers.DepositL1(a.Log(), user.ChainClients[common.BTCChain], common.BTCAsset, memo, amount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to deposit BTC for XMR swap")
		return OpResult{Continue: false}
	}

	a.swapTxID = txid
	a.Log().Info().Str("txid", txid).Str("to", xmrAddr.String()).Msg("BTC->XMR swap initiated")
	return OpResult{Continue: true}
}

func (a *XMRSpentRefVerifyActor) waitForSwapOutbound(_ *OpConfig) OpResult {
	details, result := waitForXMRSwapOutbound(a.Log(), a.swapTxID, a.getSwapStages, a.getSwapDetails)
	if !result.Continue {
		return result
	}
	if len(details.OutTxs) != 1 || details.OutTxs[0].GetChain() != common.XMRChain.String() || details.OutTxs[0].GetId() == "" {
		return OpResult{Finish: true, Error: fmt.Errorf("swap %s must produce exactly one identified XMR outbound", a.swapTxID)}
	}
	a.outboundTxID = details.OutTxs[0].GetId()
	return OpResult{Continue: true}
}

func xmrSpentOutpoint(ref common.OutputRef) string {
	return fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(ref.TxHash)), ref.OutputIndex)
}

func (a *XMRSpentRefVerifyActor) captureSpentRefs(vault string, refs []common.OutputRef) {
	a.vaultPubKey = vault
	a.spentBefore = make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		a.spentBefore[xmrSpentOutpoint(ref)] = struct{}{}
	}
}

func (a *XMRSpentRefVerifyActor) waitForSpentRefsOnVault(_ *OpConfig) OpResult {
	observed, err := thornode.GetTxSpentRefs(a.outboundTxID)
	if err != nil {
		a.Log().Error().Err(err).Msg("waiting for outbound consensus observation")
		return OpResult{}
	}
	// Query the captured vault, even if it is now retiring. A different active
	// vault or unrelated swap must never supply this outbound's evidence.
	stored, err := thornode.GetXMRSpentOutputRefs(a.vaultPubKey)
	if err != nil {
		a.Log().Error().Err(err).Msg("waiting for persisted outbound spent refs")
		return OpResult{}
	}
	return a.verifyOutboundSpentRefs(observed, stored.SpentOutputRefs)
}

func (a *XMRSpentRefVerifyActor) verifyOutboundSpentRefs(observed thornode.ObservedTxSpentRefs, stored []common.OutputRef) OpResult {
	if a.spentBefore == nil || a.vaultPubKey == "" || a.outboundTxID == "" {
		return OpResult{Finish: true, Error: fmt.Errorf("spent-ref verification requires a pre-swap vault snapshot and an outbound ID")}
	}
	tx := observed.ObservedTx
	// waitForSwapOutbound already required the matched outbound to be signed.
	// Its voter records when consensus actions ran; late final votes can replace
	// the stored Tx and reset its status to incomplete without undoing them.
	// The query endpoint omits the status field for incomplete observations.
	// Require that consensus and no reversion, then verify the same vault's
	// persisted spent refs below regardless of the observation's finality.
	if observed.ConsensusHeight <= 0 || observed.Reverted ||
		(tx.Status != "done" && tx.Status != "") || tx.Tx.GetId() == "" {
		return OpResult{}
	}
	if !strings.EqualFold(tx.Tx.GetId(), a.outboundTxID) || tx.Tx.GetChain() != common.XMRChain.String() || tx.ObservedPubKey != a.vaultPubKey {
		return OpResult{Finish: true, Error: fmt.Errorf("outbound %s observation does not match the captured XMR vault", a.outboundTxID)}
	}
	// The selected consensus observation can omit refs or carry a minority set.
	// The vault store contains the separately approved ref set bound to that
	// consensus outbound, so select its evidence by the spending transaction ID.
	var outboundRefs []common.OutputRef
	for _, ref := range stored {
		if strings.EqualFold(strings.TrimSpace(ref.SpendTxHash), a.outboundTxID) {
			outboundRefs = append(outboundRefs, ref)
		}
	}
	if len(outboundRefs) == 0 {
		return OpResult{}
	}
	if err := common.ValidateSpentOutputRefs(common.XMRChain, common.TxID(a.outboundTxID), outboundRefs); err != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("outbound %s spent refs: %w", a.outboundTxID, err)}
	}
	for _, ref := range outboundRefs {
		if _, old := a.spentBefore[xmrSpentOutpoint(ref)]; old {
			return OpResult{Finish: true, Error: fmt.Errorf("outbound %s reused previously spent output %s", a.outboundTxID, xmrSpentOutpoint(ref))}
		}
	}
	a.Log().Info().Str("outbound_txid", a.outboundTxID).Int("spent_refs", len(outboundRefs)).Msg("verified new outbound spent refs persisted on its vault")
	return OpResult{Continue: true}
}

func (a *XMRSpentRefVerifyActor) logVaultBalance(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vault balances")
		return OpResult{Continue: false}
	}

	for _, vault := range vaults {
		if vault.Status != "ActiveVault" {
			continue
		}
		for _, coin := range vault.Coins {
			if coin.Asset != "XMR.XMR" {
				continue
			}
			a.Log().Info().
				Str("vault_xmr_balance", coin.Amount).
				Msg("XMR vault balance after spent ref tracking")
		}
	}

	a.Log().Info().Msg("spent ref tracking e2e verification complete")
	return OpResult{Finish: true}
}
