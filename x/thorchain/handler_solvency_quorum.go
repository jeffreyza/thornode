package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// SolvencyQuorumHandler is to process MsgSolvencyQuorum message from bifrost
// Bifrost constantly monitor the account balance , and report to THORNode
// If it detect that wallet is short of fund , much less than vault, the network should automatically halt trading
type SolvencyQuorumHandler struct {
	mgr Manager
}

// NewSolvencyQuorumHandler create a new instance of solvency handler
func NewSolvencyQuorumHandler(mgr Manager) SolvencyQuorumHandler {
	return SolvencyQuorumHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to process MsgSolvencyQuorum
func (h SolvencyQuorumHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgSolvencyQuorum)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg solvency failed validation", "error", err)
		return nil, err
	}
	return h.handle(ctx, *msg)
}

func (h SolvencyQuorumHandler) validate(ctx cosmos.Context, msg types.MsgSolvencyQuorum) error {
	return msg.ValidateBasic()
}

// handleCurrent is the logic to process MsgSolvencyQuorum, the feature works like this
//  1. Bifrost report MsgSolvencyQuorum to thornode , which is the balance of asgard wallet on each individual chain
//  2. once MsgSolvencyQuorum reach consensus , then the network compare the wallet balance against wallet
//     if wallet has less fund than asgard vault , and the gap is more than 1% , then the chain
//     that is insolvent will be halt
//  3. When chain is halt , bifrost will not observe inbound , and will not sign outbound txs until the issue has been investigated , and enabled it again using mimir
func (h SolvencyQuorumHandler) handle(ctx cosmos.Context, msg types.MsgSolvencyQuorum) (*cosmos.Result, error) {
	if msg.QuoSolvency == nil || msg.QuoSolvency.Solvency == nil {
		return nil, cosmos.ErrUnknownRequest("QuoSolvency and Solvency cannot be nil")
	}
	s := msg.QuoSolvency.Solvency

	ctx.Logger().Debug("handle Solvency request", "id", s.Id.String(), "signer", msg.Signer.String())

	k := h.mgr.Keeper()

	active, err := k.ListActiveValidators(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}

	voter, err := k.GetSolvencyVoter(ctx, s.Id, s.Chain)
	if err != nil {
		return &cosmos.Result{}, fmt.Errorf("fail to get solvency voter, err: %w", err)
	}
	signBz, err := s.GetSignablePayload()
	if err != nil {
		// Retrying cannot repair this payload. Acknowledge it so ebifrost can
		// discard the batch, without writing a voter or consuming any quota.
		ctx.Logger().Error("fail to marshal solvency sign payload", "error", err)
		return &cosmos.Result{}, nil
	}

	// Verify before admitting a new voter. The injected transaction's signer
	// is a relayer, not the validator whose allowance should be charged.
	attestations := deduplicateAttestations(msg.QuoSolvency.Attestations, len(active))
	sigs := preverifyAttestations(active, attestationChecks(attestations, signBz))
	verified := make([]cosmos.AccAddress, 0, len(attestations))
	for _, att := range attestations {
		accAddr, verifyErr := verifyQuorumAttestation(active, signBz, att, sigs)
		if verifyErr != nil {
			ctx.Logger().Error("fail to verify quorum solvency attestation", "error", verifyErr)
			continue
		}
		verified = append(verified, accAddr)
	}
	if len(verified) == 0 {
		// Acknowledge unusable attestations so ebifrost can remove them from
		// its queue, including signatures from validators that have churned out.
		// Do not create or update a voter or consume any quota.
		return &cosmos.Result{}, nil
	}

	var quota *solvencyVoterQuota
	if voter.Empty() {
		quota, err = checkSolvencyVoterQuota(ctx, k, s.Chain, verified)
		if err != nil {
			// Failure preserves queued attestations for retry; reporting success
			// here would make the ebifrost post-handler mark them confirmed.
			return nil, err
		}
		voter = NewSolvencyVoter(s.Id, s.Chain, s.PubKey, s.Coins, s.Height)
	}

	for _, accAddr := range verified {
		if err := processSolvencyAttestation(ctx, h.mgr, &voter, accAddr, active, s, false); err != nil {
			// Propagate the error to prevent persisting a voter in inconsistent state.
			// This matches the non-quorum handler (handler_solvency.go) and network fee
			// quorum handler behavior, causing state rollback via CacheContext.
			return nil, fmt.Errorf("fail to process solvency attestation: %w", err)
		}
	}

	k.SetSolvencyVoter(ctx, voter)
	quota.commit(ctx, k)
	return &cosmos.Result{}, nil
}
