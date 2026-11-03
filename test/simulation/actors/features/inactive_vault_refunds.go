package features

import (
	"fmt"
	rand "math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// InactiveVaultRefunds
////////////////////////////////////////////////////////////////////////////////////////

func InactiveVaultRefunds(rng *rand.Rand) *Actor {
	a := NewActor("Feature-InactiveVaultRefunds", rng)

	for _, chain := range acommon.SimChains {
		if chain.Equals(common.SOLChain) { // skip SOL as inactive vaults default disabled
			continue
		}
		a.Children[NewInactiveVaultRefundActor(chain.GetGasAsset(), rng)] = true
	}

	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// InactiveVaultRefundActor
////////////////////////////////////////////////////////////////////////////////////////

type InactiveVaultRefundActor struct {
	Actor

	account          *User
	asset            common.Asset
	amount           cosmos.Uint
	inboundTxid      string
	refundAddress    common.Address
	inboundAddress   common.Address
	inboundVault     string
	memo             string
	registeredMemo   string
	registrationTxID common.TxID

	requireHistoricalRecovery bool
	recoveryVault             openapi.Vault
	recoveryMembers           map[string]string
	recoveryGate              Op
	getInactiveVaults         func() ([]openapi.Vault, error)
}

func NewInactiveVaultRefundActor(asset common.Asset, rng *rand.Rand) *Actor {
	return &newInactiveVaultRefundActor(asset, rng).Actor
}

// XMRHistoricalRefund is a deterministic five-node recovery scenario. Jailing
// one active old member requires the departed member to sign without disabling
// any node's scanner. The completed payment must use that historical policy.
func XMRHistoricalRefund(rng *rand.Rand) *Actor {
	root := NewActor("XMRHistoricalRefund", rng)
	root.Append(NewMultiNodeMimirActor("HaltChurning", 1, rng))
	a := newInactiveVaultRefundActor(common.XMRAsset, rng)
	a.requireHistoricalRecovery = true
	a.Name = "XMRHistoricalRefund-DepartedSigner"
	a.Timeout = 35 * time.Minute
	root.Append(&a.Actor)
	return root
}

func newInactiveVaultRefundActor(asset common.Asset, rng *rand.Rand) *InactiveVaultRefundActor {
	a := &InactiveVaultRefundActor{
		Actor:             *NewActor(fmt.Sprintf("InactiveVaultRefund-%s", asset), rng),
		asset:             asset,
		getInactiveVaults: thornode.GetInactiveVaults,
	}

	a.SetLogger(a.Log().With().Str("asset", asset.String()).Logger())

	// Require an eligible retired vault before acquiring funds or sending a deposit.
	a.Ops = append(a.Ops, a.selectInactiveVault)

	// lock a user that has sufficient L1 balance
	a.Ops = append(a.Ops, a.acquireUser)

	// XMR needs an explicit refund destination in a registered reference memo.
	a.Ops = append(a.Ops, a.registerRefundMemo, a.resolveRefundMemo, a.prepareHistoricalRecovery)

	// send inbound to the selected inactive vault
	a.Ops = append(a.Ops, a.sendInactiveVaultInbound)

	// verify refund outbound
	a.Ops = append(a.Ops, a.verifyRefund)

	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *InactiveVaultRefundActor) acquireUser(config *OpConfig) OpResult {
	// determine the asset amount
	pool, err := thornode.GetPool(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pool")
		return OpResult{
			Continue: false,
		}
	}

	fee, err := thornode.GetOutboundFee(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get refund outbound fee")
		return OpResult{Continue: false}
	}
	a.amount = refundAmount(cosmos.NewUintFromString(pool.BalanceAsset), fee)

	for _, user := range config.Users {
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())

		// skip users already being used
		if !user.Acquire() {
			continue
		}

		// skip users that with insufficient L1 balance
		l1Acct, err := user.ChainClients[a.asset.Chain].GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get L1 account")
			user.Release()
			continue
		}
		if l1Acct.Coins.GetCoin(a.asset).Amount.LTE(a.amount) {
			a.Log().Error().Msg("user has insufficient L1 balance")
			user.Release()
			continue
		}

		// get l1 address to store in state context
		l1Address, err := user.PubKey(a.asset.Chain).GetAddress(a.asset.Chain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get L1 address")
			user.Release()
			continue
		}

		// set acquired account and amounts in state context
		a.Log().Info().Stringer("l1Address", l1Address).Msg("acquired user")
		a.account = user
		a.refundAddress = l1Address

		break
	}

	// remain pending if no user is available
	return OpResult{
		Continue: a.account != nil,
	}
}

func (a *InactiveVaultRefundActor) selectInactiveVault(config *OpConfig) OpResult {
	if a.requireHistoricalRecovery {
		mimirs, err := thornode.GetMimirs()
		if err != nil {
			return OpResult{Error: err}
		}
		if mimirs["HALTCHURNING"] != 1 {
			return OpResult{}
		}
		nodes, err := thornode.GetNodes()
		if err != nil {
			return OpResult{Error: err}
		}
		vaults, err := a.getInactiveVaults()
		if err != nil {
			return OpResult{Error: err}
		}
		for _, vault := range vaults {
			members, ok := historicalRefundMembers(vault, nodes)
			if !ok {
				continue
			}
			for _, addr := range vault.Addresses {
				if addr.Chain != common.XMRChain.String() || addr.Address == "" {
					continue
				}
				a.inboundAddress, a.inboundVault = common.Address(addr.Address), vault.GetPubKeyFrost()
				a.recoveryVault, a.recoveryMembers = vault, members
				a.Log().Info().Str("vault", a.inboundVault).Interface("member_statuses", members).Msg("selected XMR historical refund vault")
				return OpResult{Continue: true}
			}
		}
		return OpResult{Finish: true, Error: fmt.Errorf("run cluster-churn first: historical refund requires a four-member inactive XMR vault with one departed and three active members")}
	}

	address, pubkey, err := a.findInactiveVaultAddress()
	if err != nil {
		return OpResult{Error: err}
	}
	if address.IsEmpty() || pubkey == "" {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("inactive-vault refund requires an eligible inactive %s vault; run churn first", a.asset.Chain),
		}
	}
	a.inboundAddress, a.inboundVault = address, pubkey
	return OpResult{Continue: true}
}

func (a *InactiveVaultRefundActor) registerRefundMemo(_ *OpConfig) OpResult {
	a.memo = "TEST-INACTIVE-VAULT-REFUND"
	if !a.asset.Chain.Equals(common.XMRChain) {
		return OpResult{Continue: true}
	}
	thorAddress, err := a.account.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		return OpResult{Error: err}
	}
	// The inactive-vault handler refunds this ADD instead of adding liquidity.
	// Its explicit asset address is needed because XMR has no observed sender.
	a.registeredMemo = fmt.Sprintf("+:%s:%s/%s", a.asset, thorAddress, a.refundAddress)
	msg := types.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneNative, cosmos.ZeroUint())),
		fmt.Sprintf("REFERENCE:%s:%s", a.asset, a.registeredMemo), thorAddress,
	)
	a.registrationTxID, err = a.account.Thorchain.BroadcastWithBlocking(msg)
	if err != nil {
		return OpResult{Error: err}
	}
	return OpResult{Continue: true}
}

func (a *InactiveVaultRefundActor) resolveRefundMemo(_ *OpConfig) OpResult {
	if !a.asset.Chain.Equals(common.XMRChain) {
		return OpResult{Continue: true}
	}
	registered, err := thornode.GetMemoHash(a.registrationTxID.String())
	if err != nil {
		return OpResult{Error: err}
	}
	memo, err := inactiveRefundReference(registered, a.asset, a.registeredMemo)
	if err != nil {
		return OpResult{Error: err}
	}
	a.memo = memo
	return OpResult{Continue: true}
}

func inactiveRefundReference(registered openapi.ReferenceMemoResponse, asset common.Asset, expectedMemo string) (string, error) {
	if registered.Asset != asset.String() || registered.Memo != expectedMemo {
		return "", fmt.Errorf("inactive-vault refund reference does not match the registered asset and memo")
	}
	ref, err := strconv.ParseUint(registered.Reference, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid inactive-vault refund reference: %w", err)
	}
	return fmt.Sprintf("r:%d", ref), nil
}

func (a *InactiveVaultRefundActor) sendInactiveVaultInbound(config *OpConfig) OpResult {
	client := a.account.ChainClients[a.asset.Chain]

	// create tx out
	tx := SimTx{
		Chain:     a.asset.Chain,
		ToAddress: a.inboundAddress,
		Coin:      common.NewCoin(a.asset, a.amount),
		Memo:      a.memo,
	}

	txid, err := SignAndBroadcastTx(client, tx)
	if err != nil {
		log.Error().Err(err).Msg("failed to broadcast tx")
		return SimTxErrorResult(err)
	}
	a.Log().Info().Str("txid", txid).Msg("sent inbound to inactive vault")

	a.inboundTxid = txid
	return OpResult{
		Continue: true,
	}
}

func (a *InactiveVaultRefundActor) findInactiveVaultAddress() (common.Address, string, error) {
	vaults, err := a.getInactiveVaults()
	if err != nil {
		return "", "", err
	}

	for _, vault := range vaults {
		if vault.Status != types.VaultStatus_InactiveVault.String() {
			continue
		}
		for _, address := range vault.Addresses {
			if address.Chain == a.asset.Chain.String() {
				pubkey := vault.GetPubKey()
				if a.asset.Chain.Equals(common.XMRChain) {
					pubkey = vault.GetPubKeyFrost()
				}
				if address.Address == "" || pubkey == "" {
					return "", "", fmt.Errorf("inactive %s vault has incomplete address or signing key", a.asset.Chain)
				}
				return common.Address(address.Address), pubkey, nil
			}
		}
	}

	return "", "", nil
}

func (a *InactiveVaultRefundActor) verifyRefund(config *OpConfig) OpResult {
	// get swap stages
	stages, err := thornode.GetTxStages(a.inboundTxid)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx stages")
		return OpResult{
			Continue: false,
		}
	}

	// wait for outbound to be marked complete
	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{
			Continue: false,
		}
	}

	// get tx details
	details, err := thornode.GetTxDetails(a.inboundTxid)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get tx details")
		return OpResult{
			Continue: false,
		}
	}

	// Verify the deposit reached the selected retired vault and that the
	// completed outbound really is a refund to this user's source-chain address.
	err = validateInactiveVaultRefund(details, a.asset, a.inboundTxid, a.inboundVault, a.refundAddress, a.amount)
	if err == nil && a.requireHistoricalRecovery {
		if result := a.verifyHistoricalRecovery(config, details); !result.Continue {
			if result.Finish {
				a.account.Release()
			}
			return result
		}
	}
	a.account.Release()
	return OpResult{Finish: true, Error: err}
}

func validateInactiveVaultRefund(details openapi.TxDetailsResponse, asset common.Asset, inboundTxID, inboundVault string, refundAddress common.Address, deposited cosmos.Uint) error {
	if details.Tx.GetObservedPubKey() != inboundVault {
		return fmt.Errorf("refund inbound was not observed on the selected inactive vault")
	}
	if len(details.OutTxs) != 1 || len(details.Actions) != 1 {
		return fmt.Errorf("expected exactly one refund action and outbound")
	}
	action, outbound := details.Actions[0], details.OutTxs[0]
	memo := action.GetOriginalMemo()
	if memo == "" {
		memo = action.GetMemo()
	}
	if !strings.EqualFold(action.GetInHash(), inboundTxID) || !strings.EqualFold(memo, "REFUND:"+inboundTxID) {
		return fmt.Errorf("outbound action is not the expected inactive-vault refund")
	}
	if action.Chain != asset.Chain.String() || outbound.GetChain() != asset.Chain.String() ||
		!common.Address(action.ToAddress).Equals(refundAddress) || !common.Address(outbound.GetToAddress()).Equals(refundAddress) {
		return fmt.Errorf("inactive-vault refund has the wrong chain or recipient")
	}
	if len(outbound.Coins) != 1 || outbound.Coins[0].Asset != asset.String() || action.Coin.Asset != asset.String() {
		return fmt.Errorf("inactive-vault refund has the wrong asset")
	}
	amount, err := cosmos.ParseUint(outbound.Coins[0].Amount)
	if err != nil || amount.IsZero() || amount.GT(deposited) {
		return fmt.Errorf("inactive-vault refund has an invalid amount")
	}
	return nil
}

func historicalRefundMembers(vault openapi.Vault, nodes []openapi.Node) (map[string]string, bool) {
	if vault.Status != "InactiveVault" || vault.GetPubKeyFrost() == "" || len(vault.Membership) != 4 {
		return nil, false
	}
	byKey := make(map[string]openapi.Node)
	for _, node := range nodes {
		byKey[node.PubKeySet.GetSecp256k1()] = node
	}
	statuses := make(map[string]string)
	active, departed := 0, 0
	for _, key := range vault.Membership {
		node, ok := byKey[key]
		if !ok || node.ForcedToLeave || (node.Status == "Unknown" || node.Status == "") || statuses[key] != "" {
			return nil, false
		}
		statuses[key] = node.Status
		if node.Status == "Active" {
			active++
		} else {
			departed++
		}
	}
	return statuses, active == 3 && departed == 1
}

func (a *InactiveVaultRefundActor) prepareHistoricalRecovery(config *OpConfig) OpResult {
	if !a.requireHistoricalRecovery {
		return OpResult{Continue: true}
	}
	if a.recoveryGate == nil {
		a.recoveryGate = core.NewXMRHistoricalSignerGate(a.recoveryVault)
	}
	return a.recoveryGate(config)
}

func validateHistoricalRefundPolicy(policy thorclient.KeysignPolicy, statuses map[string]string) error {
	if policy.RequiredSigners != 3 || len(policy.Signers) != 3 {
		return fmt.Errorf("refund signing policy did not force a three-member party")
	}
	departed := 0
	seen := make(map[string]bool)
	for _, key := range policy.Signers {
		status, exists := statuses[key.String()]
		if !exists || seen[key.String()] {
			return fmt.Errorf("refund policy contains a duplicate or unexpected member")
		}
		seen[key.String()] = true
		if status != "Active" {
			departed++
		}
	}
	if departed != 1 {
		return fmt.Errorf("refund could complete without a departed signer; recovery coverage is missing")
	}
	return nil
}

func (a *InactiveVaultRefundActor) verifyHistoricalRecovery(config *OpConfig, details openapi.TxDetailsResponse) OpResult {
	policy, err := config.NodeUsers[0].Thorchain.GetKeysignPolicy(common.PubKey(a.inboundVault), details.GetOutboundHeight())
	if err != nil {
		return OpResult{Error: err}
	}
	if err = validateHistoricalRefundPolicy(policy, a.recoveryMembers); err != nil {
		return OpResult{Finish: true, Error: err}
	}
	hash := details.OutTxs[0].GetId()
	// The default query falls back to outbound when no inbound voter exists.
	// Inspect the destination instead of mistaking its finalization for a credit.
	var inbound types.QueryObservedTxVoter
	endpoint := fmt.Sprintf("%s/thorchain/tx/details/%s", thornode.BaseURL(), hash)
	if err = thornode.Get(endpoint, &inbound); err != nil {
		return OpResult{Error: err}
	}
	if err = validateNoXMRRefundDeposit(inbound, a.inboundAddress); err != nil {
		return OpResult{Finish: true, Error: err}
	}
	var outbound types.QueryObservedTxVoter
	if err = thornode.Get(endpoint+"?outbound=true", &outbound); err != nil {
		return OpResult{Error: err}
	}
	if outbound.FinalisedHeight <= 0 {
		return OpResult{}
	}
	a.Log().Info().Str("txid", hash).Int64("signing_height", details.GetOutboundHeight()).
		Strs("signing_members", policy.Signers.Strings()).Interface("member_statuses", a.recoveryMembers).
		Msg("historical XMR refund finalized with a required departed signer and no false inbound credit")
	return OpResult{Continue: true}
}

func validateNoXMRRefundDeposit(voter types.QueryObservedTxVoter, source common.Address) error {
	observations := append([]types.QueryObservedTx{voter.Tx}, voter.Txs...)
	for _, observed := range observations {
		if observed.Tx.Chain.Equals(common.XMRChain) && observed.Tx.ToAddress.Equals(source) {
			return fmt.Errorf("XMR refund change was reported as an inbound: %s", voter.TxID)
		}
	}
	return nil
}
