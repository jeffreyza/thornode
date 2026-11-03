package thorchain

import (
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ObservedTxQuorumHandler to handle MsgObservedTx
type ObservedTxQuorumHandler struct {
	mgr Manager
}

// NewObservedTxQuorumHandler create a new instance of ObservedTxQuorumHandler
func NewObservedTxQuorumHandler(mgr Manager) ObservedTxQuorumHandler {
	return ObservedTxQuorumHandler{
		mgr: mgr,
	}
}

// Run is the main entry point of ObservedTxQuorumHandler
func (h ObservedTxQuorumHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgObservedTxQuorum)
	if !ok {
		return nil, errInvalidMessage
	}
	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgObservedTxQuorum failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgObservedTxQuorum message", "error", err)
	}
	return result, err
}

func (h ObservedTxQuorumHandler) validate(ctx cosmos.Context, msg types.MsgObservedTxQuorum) error {
	return msg.ValidateBasic()
}

func (h ObservedTxQuorumHandler) handle(ctx cosmos.Context, msg types.MsgObservedTxQuorum) (*cosmos.Result, error) {
	k := h.mgr.Keeper()

	activeNodeAccounts, err := k.ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}
	handler := NewInternalHandler(h.mgr)

	if msg.QuoTx == nil {
		return nil, cosmos.ErrUnknownRequest("QuoTx cannot be nil")
	}
	quoTx := msg.QuoTx
	obsTx := quoTx.ObsTx
	inbound := quoTx.Inbound

	signBz, err := obsTx.GetSignablePayloadWithInbound(inbound)
	if err != nil {
		ctx.Logger().Error("fail to marshal tx sign payload", "error", err)
		return &cosmos.Result{}, nil
	}

	var accAddrs []cosmos.AccAddress
	attestations := deduplicateAttestations(msg.QuoTx.Attestations, len(activeNodeAccounts))
	sigs := preverifyAttestations(activeNodeAccounts, attestationChecks(attestations, signBz))
	for _, att := range attestations {
		accAddr, verifyErr := verifyQuorumAttestation(activeNodeAccounts, signBz, att, sigs)
		if verifyErr != nil {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "quorum", "attestation_invalid"}, 1,
				[]metrics.Label{telemetry.NewLabel("kind", "tx")},
			)
			ctx.Logger().Error("fail to verify quorum tx in attestation", "error", verifyErr)
			continue
		}
		accAddrs = append(accAddrs, accAddr)
	}
	if len(accAddrs) == 0 {
		direction := "out"
		if inbound {
			direction = "in"
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "quorum", "tx_no_valid_attestations"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(obsTx.Tx.Chain)),
				telemetry.NewLabel("direction", direction),
			},
		)
		ctx.Logger().Info(
			"quorum tx has no active attestations",
			"chain", obsTx.Tx.Chain,
			"txid", obsTx.Tx.ID,
			"inbound", inbound,
			"attestations", len(msg.QuoTx.Attestations),
		)
		return &cosmos.Result{}, nil
	}

	// check we are sending to a valid vault
	var voter types.ObservedTxVoter
	if inbound {
		voter, err = ensureVaultAndGetTxInVoter(ctx, obsTx.ObservedPubKey, obsTx.Tx.ID, k)
		if err != nil {
			ctx.Logger().Error("fail to ensure vault and get tx in voter", "error", err)
			return &cosmos.Result{}, nil
		}
	} else {
		voter, err = ensureVaultAndGetTxOutVoter(ctx, k, obsTx.ObservedPubKey, obsTx.Tx.ID, accAddrs, obsTx.KeysignMs)
		if err != nil {
			ctx.Logger().Error("fail to ensure vault and get tx out voter", "error", err)
			return &cosmos.Result{}, nil
		}
	}

	// Apply every attestation to the voter in memory and save it once: nothing
	// applied here reads the voter back from the store, and saving it per
	// attestation rewrote the whole (growing) voter once per signer.
	var crossedQuorum, voterChanged bool
	for _, accAddr := range accAddrs {
		var isQuorum, added bool
		if inbound {
			voter, isQuorum, added = applyTxInAttestation(ctx, h.mgr, voter, activeNodeAccounts, obsTx, accAddr, false)
		} else {
			voter, isQuorum, added = applyTxOutAttestation(ctx, h.mgr, voter, activeNodeAccounts, obsTx, accAddr, false)
		}
		if isQuorum {
			// we crossed over quorum with this attestation.
			crossedQuorum = true
		}
		voterChanged = voterChanged || added
	}
	if voterChanged {
		if inbound {
			k.SetObservedTxInVoter(ctx, voter)
		} else {
			k.SetObservedTxOutVoter(ctx, voter)
		}
	}

	if inbound {
		if err := handleObservedTxInQuorum(ctx, h.mgr, msg.Signer, activeNodeAccounts, handler, obsTx, voter, accAddrs, crossedQuorum); err != nil {
			return nil, wrapError(ctx, err, "fail to handle observed tx in quorum")
		}
	} else {
		if err := handleObservedTxOutQuorum(ctx, h.mgr, msg.Signer, activeNodeAccounts, handler, obsTx, voter, accAddrs, crossedQuorum); err != nil {
			return nil, wrapError(ctx, err, "fail to handle observed tx out quorum")
		}
	}

	return &cosmos.Result{}, nil
}
