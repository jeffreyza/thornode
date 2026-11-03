package thorchain

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"
	wasm "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"
	tmhttp "github.com/cometbft/cometbft/rpc/client/http"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/evm/ethereum/eip712"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Swap status constants
const (
	SwapStatusQueued = "queued"
)

// Queue type constants
const (
	QueueTypeRegular  = "regular"
	QueueTypeAdvanced = "advanced"
)

const (
	jailReasonFailedToVoteKeygen     = "failed to vote keygen in time"
	jailReasonFailedToPerformKeygen  = "failed to perform keygen"
	jailReasonFailedToPerformKeysign = "failed to perform keysign"
)

var (
	initManager = func(_ cosmos.Context, _ *Mgrs) {}
	queryExport = func(_ sdk.Context, _ *Mgrs) ([]byte, error) {
		return nil, fmt.Errorf("export query not supported")
	}
	tendermintClient    *tmhttp.HTTP // fallback when no in-process client is set
	tendermintClientErr error
	initTendermintOnce  = sync.Once{}
)

func initTendermint() {
	// get tendermint port from config
	portSplit := strings.Split(config.GetThornode().Tendermint.RPC.ListenAddress, ":")
	port := portSplit[len(portSplit)-1]

	// setup tendermint client
	var err error
	tendermintClient, err = tmhttp.New(fmt.Sprintf("tcp://localhost:%s", port), "/websocket")
	if err != nil {
		// reachable from a query - return the error to the caller rather than
		// taking the process down
		tendermintClientErr = fmt.Errorf("fail to create tendermint client: %w", err)
		log.Error().Err(err).Msg("fail to create tendermint client")
	}
}

func getPeerIDFromPubKey(pubkey common.PubKey) string {
	peerID, err := conversion.GetPeerIDFromPubKey(pubkey.String())
	if err != nil {
		// Don't break the entire endpoint if something goes wrong with the Peer ID derivation.
		return err.Error()
	}

	return peerID.String()
}

func (qs queryServer) queryRagnarok(ctx cosmos.Context, _ *types.QueryRagnarokRequest) (*types.QueryRagnarokResponse, error) {
	ragnarokInProgress := qs.mgr.Keeper().RagnarokInProgress(ctx)
	return &types.QueryRagnarokResponse{InProgress: ragnarokInProgress}, nil
}

func (qs queryServer) queryBalanceModule(ctx cosmos.Context, req *types.QueryBalanceModuleRequest) (*types.QueryBalanceModuleResponse, error) {
	moduleName := req.Name
	if len(moduleName) == 0 {
		moduleName = AsgardName
	}

	modAddr := qs.mgr.Keeper().GetModuleAccAddress(moduleName)
	bal := qs.mgr.Keeper().GetBalance(ctx, modAddr)
	balance := types.QueryBalanceModuleResponse{
		Name:    moduleName,
		Address: modAddr,
		Coins:   bal,
	}
	return &balance, nil
}

func (qs queryServer) queryTHORName(ctx cosmos.Context, req *types.QueryThornameRequest) (*types.QueryThornameResponse, error) {
	name, err := qs.mgr.Keeper().GetTHORName(ctx, req.Name)
	if err != nil {
		return nil, ErrInternal(err, "fail to fetch THORName")
	}

	affRune := cosmos.ZeroUint()
	affCol, err := qs.mgr.Keeper().GetAffiliateCollector(ctx, name.Name)
	if err == nil {
		affRune = affCol.RuneAmount
	}

	// convert to openapi types
	aliases := []*types.ThornameAlias{}
	for _, alias := range name.Aliases {
		aliases = append(aliases, &types.ThornameAlias{
			Chain:   alias.Chain.String(),
			Address: alias.Address.String(),
		})
	}

	threshold := cosmos.ZeroUint()
	if !name.PreferredAsset.IsEmpty() {
		paOf, err := qs.mgr.GasMgr().GetAssetOutboundFee(ctx, name.PreferredAsset, true)
		if err == nil {
			multiplier := getEffectiveMultiplier(ctx, qs.mgr, name)
			threshold = paOf.MulUint64(uint64(multiplier))
		}
	}

	resp := types.QueryThornameResponse{
		Name:                                name.Name,
		ExpireBlockHeight:                   name.ExpireBlockHeight,
		Owner:                               name.Owner.String(),
		PreferredAsset:                      name.PreferredAsset.String(),
		Aliases:                             aliases,
		AffiliateCollectorRune:              affRune.String(),
		PreferredAssetSwapThresholdRune:     threshold.String(),
		PreferredAssetOutboundFeeMultiplier: name.PreferredAssetOutboundFeeMultiplier,
	}

	return &resp, nil
}

func (qs queryServer) queryVault(ctx cosmos.Context, req *types.QueryVaultRequest) (*types.QueryVaultResponse, error) {
	if len(req.PubKey) < 1 {
		return nil, errors.New("missing vault pub_key parameter")
	}
	pubkey, err := common.NewPubKey(req.PubKey)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid pubkey", req.PubKey)
	}
	v, err := qs.mgr.Keeper().GetVault(ctx, pubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault with pubkey(%s),err:%w", pubkey, err)
	}
	if v.IsEmpty() {
		return nil, errors.New("vault not found")
	}

	resp := types.QueryVaultResponse{
		BlockHeight:            v.BlockHeight,
		PubKey:                 v.PubKey.String(),
		PubKeyEddsa:            v.PubKeyEddsa.String(),
		PubKeyFrost:            v.PubKeyFrost.String(),
		Coins:                  v.Coins,
		Deficit:                v.Deficit,
		Type:                   v.Type.String(),
		Status:                 v.Status.String(),
		StatusSince:            v.StatusSince,
		Membership:             v.Membership,
		Chains:                 v.Chains,
		InboundTxCount:         v.InboundTxCount,
		OutboundTxCount:        v.OutboundTxCount,
		PendingTxBlockHeights:  v.PendingTxBlockHeights,
		Routers:                castVaultRouters(v.Routers),
		Addresses:              getVaultChainAddresses(ctx, v),
		Frozen:                 v.Frozen,
		VaultCreationHeightXmr: v.VaultCreationHeightXmr,
	}
	return &resp, nil
}

// queryKeysignParty classifies vault members into active, never-sign, and
// jailed-fallback signers at a specific THORChain height.
func (qs queryServer) queryKeysignParty(ctx cosmos.Context, req *types.QueryKeysignPartyRequest) (*types.QueryKeysignPartyResponse, error) {
	if len(req.PubKey) < 1 {
		return nil, errors.New("missing vault pub_key parameter")
	}
	height, err := optionalQueryHeight(ctx, req.SigningHeight)
	if err != nil {
		return nil, err
	}
	// Evaluate jail release state at the requested signing height. The query
	// still reads the current store, so callers use rounded heights and avoid
	// binding the returned party into keysign message IDs.
	ctx = ctx.WithBlockHeight(height)

	pubkey, err := common.NewPubKey(req.PubKey)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid pubkey", req.PubKey)
	}
	vault, err := qs.mgr.Keeper().GetVault(ctx, pubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault with pubkey(%s),err:%w", pubkey, err)
	}
	if vault.IsEmpty() {
		return nil, errors.New("vault not found")
	}
	compromised, err := isVaultCompromised(ctx, qs.mgr.Keeper(), vault.PubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get compromised vault mimir: %w", err)
	}
	// A compromised vault is a vault-level hard stop; no member fallback should
	// form a signing party for it.
	if compromised {
		return nil, fmt.Errorf("vault %s is %w", vault.PubKey.String(), common.ErrCompromisedVault)
	}
	members := vault.GetMembership()
	if len(members) == 0 {
		return nil, fmt.Errorf("vault %s has empty signer membership", pubkey.String())
	}

	threshold, err := conversion.GetThreshold(len(members))
	if err != nil {
		return nil, fmt.Errorf("fail to calculate key sign threshold: %w", err)
	}
	requiredSigners := threshold + 1
	policy := keysignPartyPolicy{signers: members}
	filteringDisabled, err := tssSignerFilteringDisabled(ctx, qs.mgr.Keeper())
	if err != nil {
		return nil, fmt.Errorf("fail to get TSS signer filtering mimir: %w", err)
	}
	if !filteringDisabled {
		policy, err = keysignPolicyForVaultMembers(ctx, qs.mgr.Keeper(), members, requiredSigners)
		if err != nil {
			return nil, fmt.Errorf("fail to classify key sign party for vault %s: %w", pubkey.String(), err)
		}
	}

	return &types.QueryKeysignPartyResponse{
		PubKey:                vault.PubKey.String(),
		Signers:               policy.signers.Strings(),
		Height:                height,
		Threshold:             int64(threshold),
		RequiredSigners:       int64(requiredSigners),
		NeverSigners:          policy.neverSigners.Strings(),
		JailedFallbackSigners: policy.jailedFallbackSigners.Strings(),
		ActiveSignerCount:     int64(len(policy.signers)),
	}, nil
}

// optionalQueryHeight returns the request height when supplied, otherwise the
// current context height. Future heights are rejected because jail release
// checks would otherwise be evaluated against unavailable state.
func optionalQueryHeight(ctx cosmos.Context, heightStr string) (int64, error) {
	return parseQueryHeight(ctx, heightStr, true)
}

// keysignJailedCandidate carries a deterministic fallback ordering for jailed
// vault members that may be needed to satisfy threshold.
type keysignJailedCandidate struct {
	pubkey            common.PubKey
	priority          int
	slashPoints       int64
	jailReleaseHeight int64
}

// keysignPartyMemberState is the local classification input for one vault
// member.
type keysignPartyMemberState struct {
	pubkey            common.PubKey
	never             bool
	jailed            bool
	jailReason        string
	jailReleaseHeight int64
	slashPoints       int64
}

// keysignPartyPolicy is the final signer policy returned to bifrost callers.
type keysignPartyPolicy struct {
	signers               common.PubKeys
	neverSigners          common.PubKeys
	jailedFallbackSigners common.PubKeys
}

// keysignPolicyForVaultMembers loads node and jail state for each vault member
// before applying the keysign participant policy.
func keysignPolicyForVaultMembers(ctx cosmos.Context, k keeper.Keeper, members common.PubKeys, requiredSigners int) (keysignPartyPolicy, error) {
	states := make([]keysignPartyMemberState, 0, len(members))
	for _, member := range members {
		nodeAccount, err := k.GetNodeAccountByPubKey(ctx, member)
		if err != nil {
			return keysignPartyPolicy{}, fmt.Errorf("vault member %s is missing node account state: %w", member.String(), err)
		}
		jail, err := k.GetNodeAccountJail(ctx, nodeAccount.NodeAddress)
		if err != nil {
			return keysignPartyPolicy{}, fmt.Errorf("fail to get node account jail for vault member %s: %w", member.String(), err)
		}
		slashPoints, err := k.GetNodeAccountSlashPoints(ctx, nodeAccount.NodeAddress)
		if err != nil {
			return keysignPartyPolicy{}, fmt.Errorf("fail to get slash points for vault member %s: %w", member.String(), err)
		}
		states = append(states, keysignPartyMemberState{
			pubkey:            member,
			never:             nodeAccountForcedOrFlagged(nodeAccount),
			jailed:            jail.IsJailed(ctx),
			jailReason:        jail.Reason,
			jailReleaseHeight: jail.ReleaseHeight,
			slashPoints:       slashPoints,
		})
	}
	return classifyKeysignParty(states, requiredSigners)
}

// classifyKeysignParty prefers non-jailed vault members, then records jailed
// members as deterministic fallback candidates that are used only when required.
func classifyKeysignParty(members []keysignPartyMemberState, requiredSigners int) (keysignPartyPolicy, error) {
	preferred := make(common.PubKeys, 0, len(members))
	fallbackJailed := make([]keysignJailedCandidate, 0)
	neverSigners := make(common.PubKeys, 0)
	for _, member := range members {
		// Forced-to-leave members are always excluded. Jailed members remain
		// deterministic fallback candidates so a vault can still sign at
		// threshold when clean members alone are insufficient.
		if member.never {
			neverSigners = append(neverSigners, member.pubkey)
			continue
		}
		if member.jailed {
			fallbackJailed = append(fallbackJailed, keysignJailedCandidate{
				pubkey:            member.pubkey,
				priority:          keysignJailFallbackPriority(member.jailReason),
				slashPoints:       member.slashPoints,
				jailReleaseHeight: member.jailReleaseHeight,
			})
			continue
		}
		preferred = append(preferred, member.pubkey)
	}

	sort.SliceStable(fallbackJailed, func(i, j int) bool {
		// Use deterministic reliability signals before pubkey so every node
		// derives the same fallback set while preferring less risky signers.
		if fallbackJailed[i].priority != fallbackJailed[j].priority {
			return fallbackJailed[i].priority < fallbackJailed[j].priority
		}
		if fallbackJailed[i].slashPoints != fallbackJailed[j].slashPoints {
			return fallbackJailed[i].slashPoints < fallbackJailed[j].slashPoints
		}
		if fallbackJailed[i].jailReleaseHeight != fallbackJailed[j].jailReleaseHeight {
			return fallbackJailed[i].jailReleaseHeight < fallbackJailed[j].jailReleaseHeight
		}
		return fallbackJailed[i].pubkey.String() < fallbackJailed[j].pubkey.String()
	})
	jailedFallbackSigners := make(common.PubKeys, 0, len(fallbackJailed))
	for _, candidate := range fallbackJailed {
		jailedFallbackSigners = append(jailedFallbackSigners, candidate.pubkey)
	}

	if len(preferred) >= requiredSigners {
		return keysignPartyPolicy{
			signers:               preferred,
			neverSigners:          neverSigners,
			jailedFallbackSigners: jailedFallbackSigners,
		}, nil
	}

	needed := requiredSigners - len(preferred)
	if len(fallbackJailed) < needed {
		return keysignPartyPolicy{}, fmt.Errorf(
			"not enough eligible signers: need %d of %d, got %d preferred and %d jailed fallback after excluding %d never-eligible members",
			requiredSigners,
			len(members),
			len(preferred),
			len(fallbackJailed),
			len(neverSigners),
		)
	}
	eligible := append(common.PubKeys{}, preferred...)
	for _, candidate := range fallbackJailed[:needed] {
		eligible = append(eligible, candidate.pubkey)
	}
	return keysignPartyPolicy{
		signers:               eligible,
		neverSigners:          neverSigners,
		jailedFallbackSigners: jailedFallbackSigners,
	}, nil
}

// keysignJailFallbackPriority orders jailed fallback candidates from least to
// most directly relevant to keysign failure. Unknown reasons are still usable,
// but only after known keygen/keysign reasons.
func keysignJailFallbackPriority(reason string) int {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case jailReasonFailedToVoteKeygen:
		return 10
	case jailReasonFailedToPerformKeygen:
		return 20
	case jailReasonFailedToPerformKeysign:
		return 30
	default:
		return 40
	}
}

func (qs queryServer) queryAsgardVaults(ctx cosmos.Context, _ *types.QueryAsgardVaultsRequest) (*types.QueryAsgardVaultsResponse, error) {
	vaults, err := qs.mgr.Keeper().GetAsgardVaults(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get asgard vaults: %w", err)
	}

	// Inactive Asgard vaults are removed from the Asgard index, but every vault
	// carrying an unsettled deficit must remain observable until the deficit is
	// resolved. Preserve the existing indexed order and append only missing
	// deficit-bearing Asgard vaults from the canonical vault store.
	indexed := make(map[string]struct{}, len(vaults))
	for _, vault := range vaults {
		indexed[vault.PubKey.String()] = struct{}{}
	}
	iter := qs.mgr.Keeper().GetVaultIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var vault Vault
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &vault); err != nil {
			return nil, fmt.Errorf("fail to unmarshal vault: %w", err)
		}
		if !vault.IsAsgard() || !vault.HasDeficit() {
			continue
		}
		if _, ok := indexed[vault.PubKey.String()]; ok {
			continue
		}
		vaults = append(vaults, vault)
		indexed[vault.PubKey.String()] = struct{}{}
	}

	var vaultsWithFunds []*types.QueryVaultResponse
	for _, vault := range vaults {
		if vault.Status == InactiveVault && !vault.HasDeficit() {
			continue
		}
		if !vault.IsAsgard() {
			continue
		}
		// Being in a RetiringVault blocks a node from unbonding, so display them even if having no funds.
		if vault.HasFunds() || vault.HasDeficit() || vault.Status == ActiveVault || vault.Status == RetiringVault {
			vaultsWithFunds = append(vaultsWithFunds, &types.QueryVaultResponse{
				BlockHeight:            vault.BlockHeight,
				PubKey:                 vault.PubKey.String(),
				PubKeyEddsa:            vault.PubKeyEddsa.String(),
				PubKeyFrost:            vault.PubKeyFrost.String(),
				Coins:                  vault.Coins,
				Deficit:                vault.Deficit,
				Type:                   vault.Type.String(),
				Status:                 vault.Status.String(),
				StatusSince:            vault.StatusSince,
				Membership:             vault.Membership,
				Chains:                 vault.Chains,
				InboundTxCount:         vault.InboundTxCount,
				OutboundTxCount:        vault.OutboundTxCount,
				PendingTxBlockHeights:  vault.PendingTxBlockHeights,
				Routers:                castVaultRouters(vault.Routers),
				Frozen:                 vault.Frozen,
				Addresses:              getVaultChainAddresses(ctx, vault),
				VaultCreationHeightXmr: vault.VaultCreationHeightXmr,
			})
		}
	}

	return &types.QueryAsgardVaultsResponse{AsgardVaults: vaultsWithFunds}, nil
}

func (qs queryServer) queryXMRSpentOutputRefs(ctx cosmos.Context, req *types.QueryXMRSpentOutputRefsRequest) (*types.QueryXMRSpentOutputRefsResponse, error) {
	if len(req.VaultPubKey) < 1 {
		return nil, errors.New("missing vault_pub_key parameter")
	}
	pubkey, err := common.NewPubKey(req.VaultPubKey)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid pubkey", req.VaultPubKey)
	}
	refs, err := qs.mgr.Keeper().GetXMRSpentOutputRefs(ctx, pubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to get xmr spent refs for vault(%s),err:%w", pubkey, err)
	}
	return &types.QueryXMRSpentOutputRefsResponse{SpentOutputRefs: castOutputRefs(refs)}, nil
}

// querySolVaultNonce returns a Solana vault's committed durable nonce, or an empty
// response when it has none. Bifrost reads it at the keysign height (?height=) so
// every keysign party builds from the same value.
func (qs queryServer) querySolVaultNonce(ctx cosmos.Context, req *types.QuerySolVaultNonceRequest) (*types.QuerySolVaultNonceResponse, error) {
	if len(req.VaultPubKey) < 1 {
		return nil, errors.New("missing vault_pub_key parameter")
	}
	pubkey, err := common.NewPubKey(req.VaultPubKey)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid pubkey", req.VaultPubKey)
	}
	// Records are keyed by the vault's canonical pubkey; accept its EdDSA key too.
	if vault, vErr := qs.mgr.Keeper().GetVault(ctx, pubkey); vErr == nil && !vault.PubKey.IsEmpty() {
		pubkey = vault.PubKey
	}
	record, found, err := activeSolVaultNonce(ctx, qs.mgr.Keeper(), pubkey)
	if err != nil {
		return nil, fmt.Errorf("fail to get sol vault nonce for vault(%s),err:%w", pubkey, err)
	}
	if !found {
		return &types.QuerySolVaultNonceResponse{}, nil
	}
	return &types.QuerySolVaultNonceResponse{
		NonceAccount:  record.NonceAccount,
		Nonce:         record.Nonce,
		Slot:          record.Slot,
		UpdatedHeight: record.UpdatedHeight,
		SetByTx:       record.SetByTx.String(),
	}, nil
}

func getVaultChainAddresses(ctx cosmos.Context, vault Vault) []*types.VaultAddress {
	var result []*types.VaultAddress
	allChains := append(vault.GetChains(), common.THORChain)
	for _, c := range allChains.Distinct() {
		if c.GetSigningAlgo() == common.SigningAlgoEd25519 {
			if c == common.XMRChain {
				if vault.PubKeyFrost.IsEmpty() {
					continue
				}
			} else if vault.PubKeyEddsa.IsEmpty() {
				// this is an eddsa chain, but the vault doesn't have an eddsa pubkey, skip.
				continue
			}
		}
		addr, err := vault.GetAddress(c)
		if err != nil {
			ctx.Logger().Error("fail to get address", "chain", c.String(), "error", err)
			continue
		}

		// use tex address for deposits
		if c == common.ZECChain {
			addr, err = addr.ToTexAddress()
			if err != nil {
				ctx.Logger().Error("fail convert address", "chain", c.String(), "error", err)
				continue
			}
		}

		result = append(result,
			&types.VaultAddress{
				Chain:   c.String(),
				Address: addr.String(),
			})
	}
	return result
}

// TODO: remove these vault pubkeys once we are done attempting recoveries from them
var whitelistPubkeys = map[string]bool{
	"thorpub1addwnpepqdc348zt7v8pqrncxzf0gz47jz5jcey9tcfpvv7zlsj50qfgmw7nuj296rh": true,
	"thorpub1addwnpepqwku796ak4ke2hj356m8yfq549m3fs96t57sukgg9we7u6tsqs6ajrsandz": true,
	"thorpub1addwnpepqwm8e5jdyjkm43hlf9mkm38vqvhj8cf6l3y9a745cn076u223vvyzxlaz2u": true,
	"thorpub1addwnpepqwht8xtersz90wsyu6fgx9tj5s3lx4w6q5adcm3mxzetgaa3rxv9sshhjmu": true,
}

func (qs queryServer) queryVaultsPubkeys(ctx cosmos.Context, _ *types.QueryVaultsPubkeysRequest) (*types.QueryVaultsPubkeysResponse, error) {
	var resp types.QueryVaultsPubkeysResponse
	resp.Asgard = make([]*types.VaultInfo, 0)
	resp.Inactive = make([]*types.VaultInfo, 0)
	iter := qs.mgr.Keeper().GetVaultIterator(ctx)

	active, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, err
	}
	cutOffAge := ctx.BlockHeight() - config.GetThornode().VaultPubkeysCutoffBlocks
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var vault Vault
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &vault); err != nil {
			ctx.Logger().Error("fail to unmarshal vault", "error", err)
			return nil, fmt.Errorf("fail to unmarshal vault: %w", err)
		}
		if vault.IsAsgard() {
			switch vault.Status {
			case ActiveVault, RetiringVault:
				resp.Asgard = append(resp.Asgard, &types.VaultInfo{
					PubKey:      vault.PubKey.String(),
					PubKeyEddsa: vault.PubKeyEddsa.String(),
					PubKeyFrost: vault.PubKeyFrost.String(),
					Routers:     castVaultRouters(vault.Routers),
					Membership:  vault.Membership,
				})
			case InactiveVault:
				// skip inactive vaults that have never received an inbound
				if vault.InboundTxCount == 0 {
					continue
				}

				// skip inactive vaults older than the cutoff age
				if vault.BlockHeight < cutOffAge && !whitelistPubkeys[vault.PubKey.String()] {
					continue
				}

				activeMembers, err := vault.GetMembers(active.GetNodeAddresses())
				if err != nil {
					ctx.Logger().Error("fail to get active members of vault", "error", err)
					continue
				}
				allMembers := vault.Membership
				if HasSuperMajority(len(activeMembers), len(allMembers)) {
					resp.Inactive = append(resp.Inactive, &types.VaultInfo{
						PubKey:      vault.PubKey.String(),
						PubKeyEddsa: vault.PubKeyEddsa.String(),
						PubKeyFrost: vault.PubKeyFrost.String(),
						Routers:     castVaultRouters(vault.Routers),
						Membership:  vault.Membership,
					})
				}
			}
		}
	}
	return &resp, nil
}

func (qs queryServer) queryVaultSolvency(ctx cosmos.Context, _ *types.QueryVaultSolvencyRequest) (*types.QueryVaultSolvencyResponse, error) {
	// Get the network manager to calculate solvency
	networkMgr, ok := qs.mgr.NetworkMgr().(*NetworkMgr)
	if !ok {
		return nil, fmt.Errorf("network manager is not the correct type")
	}

	// Calculate network solvency (both over and under-solvency)
	solvencyAmounts, err := networkMgr.calculateNetworkSolvency(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate network solvency: %w", err)
	}

	// Build response with solvency per asset
	// Positive amounts indicate over-solvency, negative amounts indicate under-solvency
	resp := &types.QueryVaultSolvencyResponse{
		Assets: make([]*types.VaultSolvencyAsset, 0, len(solvencyAmounts)),
	}

	for _, assetAmt := range solvencyAmounts {
		resp.Assets = append(resp.Assets, &types.VaultSolvencyAsset{
			Asset:  assetAmt.Asset,
			Amount: assetAmt.Amount,
		})
	}

	return resp, nil
}

func (qs queryServer) queryRUNEPool(ctx cosmos.Context, _ *types.QueryRunePoolRequest) (*types.QueryRunePoolResponse, error) {
	// gather pol data
	pol, err := qs.mgr.Keeper().GetPOL(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get POL: %w", err)
	}
	polValue, err := polPoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to fetch POL value: %w", err)
	}
	pnl := pol.PnL(polValue)

	// gather runepool data
	runePool, err := qs.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get RUNE pool: %w", err)
	}

	// calculate pending units
	runePoolValue, err := runePoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to get rune pool value: %w", err)
	}
	pendingRune := qs.mgr.Keeper().GetRuneBalanceOfModule(ctx, RUNEPoolName)
	pendingUnits := common.GetSafeShare(pendingRune, runePoolValue, runePool.TotalUnits())

	// calculate provider shares
	providerValue := common.GetSafeShare(runePool.PoolUnits, runePool.TotalUnits(), runePoolValue)
	providerPnl := sdkmath.NewIntFromBigInt(providerValue.BigInt()).Sub(runePool.CurrentDeposit())

	// calculate reserve shares
	reserveValue := common.GetSafeShare(runePool.ReserveUnits, runePool.TotalUnits(), runePoolValue)
	reserveCurrentDeposit := pol.CurrentDeposit().
		Sub(runePool.CurrentDeposit()).
		Add(cosmos.NewIntFromBigInt(pendingRune.BigInt()))
	reservePnl := sdkmath.NewIntFromBigInt(reserveValue.BigInt()).Sub(reserveCurrentDeposit)

	result := types.QueryRunePoolResponse{
		Pol: &types.POL{
			RuneDeposited:  pol.RuneDeposited.String(),
			RuneWithdrawn:  pol.RuneWithdrawn.String(),
			Value:          polValue.String(),
			Pnl:            pnl.String(),
			CurrentDeposit: pol.CurrentDeposit().String(),
		},
		Providers: &types.RunePoolProviders{
			Units:          runePool.PoolUnits.String(),
			PendingUnits:   pendingUnits.String(),
			PendingRune:    pendingRune.String(),
			Value:          providerValue.String(),
			Pnl:            providerPnl.String(),
			CurrentDeposit: runePool.CurrentDeposit().String(),
		},
		Reserve: &types.RunePoolReserve{
			Units:          runePool.ReserveUnits.String(),
			Value:          reserveValue.String(),
			Pnl:            reservePnl.String(),
			CurrentDeposit: reserveCurrentDeposit.String(),
		},
	}

	return &result, nil
}

// queryRUNEProvider
func (qs queryServer) queryRUNEProvider(ctx cosmos.Context, req *types.QueryRuneProviderRequest) (*types.QueryRuneProviderResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, errors.New("unable to decode address")
	}
	rp, err := qs.mgr.Keeper().GetRUNEProvider(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("unable to GetRUNEProvider: %s", err)
	}

	// get runepool value to determine current value and pnl
	runePool, err := qs.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get RUNE pool: %w", err)
	}
	runePoolValue, err := runePoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to get rune pool value: %w", err)
	}
	providerValue := common.GetSafeShare(rp.Units, runePool.TotalUnits(), runePoolValue)
	providerPnl := providerValue.BigInt()
	providerPnl.Sub(providerPnl, rp.DepositAmount.BigInt())
	providerPnl.Add(providerPnl, rp.WithdrawAmount.BigInt())

	result := types.QueryRuneProviderResponse{
		RuneAddress:        rp.RuneAddress.String(),
		Units:              rp.Units.String(),
		Value:              providerValue.String(),
		Pnl:                providerPnl.String(),
		DepositAmount:      rp.DepositAmount.String(),
		WithdrawAmount:     rp.WithdrawAmount.String(),
		LastDepositHeight:  rp.LastDepositHeight,
		LastWithdrawHeight: rp.LastWithdrawHeight,
	}
	return &result, nil
}

// queryRUNEProviders
func (qs queryServer) queryRUNEProviders(ctx cosmos.Context, _ *types.QueryRuneProvidersRequest) (*types.QueryRuneProvidersResponse, error) {
	// get runepool value to determine current value and pnl
	runePool, err := qs.mgr.Keeper().GetRUNEPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get RUNE pool: %w", err)
	}
	runePoolValue, err := runePoolValue(ctx, qs.mgr)
	if err != nil {
		return nil, fmt.Errorf("fail to get rune pool value: %w", err)
	}

	var runeProviders []*types.QueryRuneProviderResponse
	iterator := qs.mgr.Keeper().GetRUNEProviderIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var rp types.RUNEProvider
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &rp)

		providerValue := common.GetSafeShare(rp.Units, runePool.TotalUnits(), runePoolValue)
		providerPnl := providerValue.BigInt()
		providerPnl.Sub(providerPnl, rp.DepositAmount.BigInt())
		providerPnl.Add(providerPnl, rp.WithdrawAmount.BigInt())

		runeProviders = append(runeProviders, &types.QueryRuneProviderResponse{
			RuneAddress:        rp.RuneAddress.String(),
			Units:              rp.Units.String(),
			Value:              providerValue.String(),
			Pnl:                providerPnl.String(),
			DepositAmount:      rp.DepositAmount.String(),
			WithdrawAmount:     rp.WithdrawAmount.String(),
			LastDepositHeight:  rp.LastDepositHeight,
			LastWithdrawHeight: rp.LastWithdrawHeight,
		})
	}
	return &types.QueryRuneProvidersResponse{Providers: runeProviders}, nil
}

func (qs queryServer) queryNetwork(ctx cosmos.Context, _ *types.QueryNetworkRequest) (*types.QueryNetworkResponse, error) {
	data, err := qs.mgr.Keeper().GetNetwork(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get network", "error", err)
		return nil, fmt.Errorf("fail to get network: %w", err)
	}

	vaults, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return nil, fmt.Errorf("fail to get retiring vaults: %w", err)
	}
	vaultsMigrating := (len(vaults) != 0)
	xmrActiveVaultReady, err := getXMRActiveVaultReady(ctx, qs.mgr.Keeper())
	if err != nil {
		return nil, fmt.Errorf("fail to get xmr active vault readiness: %w", err)
	}

	nodeAccounts, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get active validators: %w", err)
	}

	_, availablePoolsRune, err := getAvailablePoolsRune(ctx, qs.mgr.Keeper())
	if err != nil {
		return nil, fmt.Errorf("fail to get available pools rune: %w", err)
	}
	vaultsLiquidityRune, err := getVaultsLiquidityRune(ctx, qs.mgr.Keeper())
	if err != nil {
		return nil, fmt.Errorf("fail to get vaults liquidity rune: %w", err)
	}
	effectiveSecurityBond := getEffectiveSecurityBond(nodeAccounts)

	targetOutboundFeeSurplus := qs.mgr.Keeper().GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)
	maxMultiplierBasisPoints := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)
	minMultiplierBasisPoints := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)
	outboundFeeMultiplier := qs.mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(uint64(targetOutboundFeeSurplus)), cosmos.NewUint(data.OutboundGasSpentRune), cosmos.NewUint(data.OutboundGasWithheldRune), cosmos.NewUint(uint64(maxMultiplierBasisPoints)), cosmos.NewUint(uint64(minMultiplierBasisPoints)))

	assets := qs.mgr.Keeper().GetAnchors(ctx, common.TOR)
	median := qs.mgr.Keeper().AnchorMedian(ctx, assets).QuoUint64(constants.DollarMulti)

	result := types.QueryNetworkResponse{
		// Due to using openapi. this will be displayed in alphabetical order,
		// so its schema (and order here) should also be in alphabetical order.
		BondRewardRune:        data.BondRewardRune.String(),
		TotalBondUnits:        data.TotalBondUnits.String(),
		AvailablePoolsRune:    availablePoolsRune.String(),
		VaultsLiquidityRune:   vaultsLiquidityRune.String(),
		EffectiveSecurityBond: effectiveSecurityBond.String(),
		TotalReserve:          qs.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).String(),
		VaultsMigrating:       vaultsMigrating,
		GasSpentRune:          cosmos.NewUint(data.OutboundGasSpentRune).String(),
		GasWithheldRune:       cosmos.NewUint(data.OutboundGasWithheldRune).String(),
		OutboundFeeMultiplier: outboundFeeMultiplier.String(),
		NativeTxFeeRune:       qs.mgr.Keeper().GetNativeTxFee(ctx).String(),
		NativeOutboundFeeRune: qs.mgr.Keeper().GetOutboundTxFee(ctx).String(),
		TnsRegisterFeeRune:    qs.mgr.Keeper().GetTHORNameRegisterFee(ctx).String(),
		TnsFeePerBlockRune:    qs.mgr.Keeper().GetTHORNamePerBlockFee(ctx).String(),
		RunePriceInTor:        dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper()).String(),
		TorPriceInRune:        runePerDollarIgnoreHalt(ctx, qs.mgr.Keeper()).String(),
		TorPriceHalted:        median.IsZero(),
		XmrActiveVaultReady:   xmrActiveVaultReady,
	}

	return &result, nil
}

func getXMRActiveVaultReady(ctx cosmos.Context, k keeper.Keeper) (bool, error) {
	activeVaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return false, err
	}
	for _, vault := range activeVaults {
		// "ready" should mean THORChain has observed an active XMR vault with a
		// usable inbound address and actual XMR funds on the vault, not just that
		// an active vault record exists for the chain.
		if vault.GetCoin(common.XMRAsset).Amount.IsZero() {
			continue
		}
		if _, addrErr := vault.GetAddress(common.XMRChain); addrErr != nil {
			continue
		}
		return true, nil
	}
	return false, nil
}

// filterCompromisedVaults removes any vault whose CompromisedVault-<pubkey>
// mimir is set to a positive value. On KV error the vault is excluded
// (fail-closed): we can't confirm safety, so we treat it as compromised.
// Only called for non-WASM paths; WASM contracts must see a deterministic,
// unfiltered response to avoid consensus divergence mid-upgrade.
func filterCompromisedVaults(ctx cosmos.Context, k keeper.Keeper, active Vaults) Vaults {
	safe := make(Vaults, 0, len(active))
	for _, v := range active {
		compromised, err := isVaultCompromised(ctx, k, v.PubKey)
		if err != nil {
			ctx.Logger().Error("failed to read CompromisedVault mimir, excluding vault", "pub_key", v.PubKey.String(), "error", err)
			continue
		}
		if compromised {
			ctx.Logger().Info("excluding compromised vault", "pub_key", v.PubKey.String())
			continue
		}
		safe = append(safe, v)
	}
	return safe
}

// filterInsolventVaults removes any vault flagged insolvent by the solvency
// checker (SolvencyHaltVault-<pubkey>-<chain>). Insolvency on one chain is
// usually the symptom of a compromised key, so the whole vault is dropped from
// inbound-address election - including its still-solvent, unhalted chains -
// rather than only the halted chain. On KV error the vault is excluded
// (fail-closed). Like filterCompromisedVaults, only used for non-WASM paths so
// WASM contracts keep seeing a deterministic, unfiltered response.
func filterInsolventVaults(ctx cosmos.Context, k keeper.Keeper, active Vaults) Vaults {
	safe := make(Vaults, 0, len(active))
	for _, v := range active {
		insolvent, err := isVaultInsolvent(ctx, k, v)
		if err != nil {
			ctx.Logger().Error("failed to read solvency-halt vault mimir, excluding vault", "pub_key", v.PubKey.String(), "error", err)
			continue
		}
		if insolvent {
			ctx.Logger().Info("excluding insolvent vault from inbound election", "pub_key", v.PubKey.String())
			continue
		}
		safe = append(safe, v)
	}
	return safe
}

func (qs queryServer) queryInboundAddresses(ctx cosmos.Context, _ *types.QueryInboundAddressesRequest) (*types.QueryInboundAddressesResponse, error) {
	active, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("fail to get active vaults", "error", err)
		return nil, fmt.Errorf("fail to get active vaults: %w", err)
	}

	isWasm, _ := ctx.Value(constants.CtxWASMQuery).(bool)
	if !isWasm {
		active = filterCompromisedVaults(ctx, qs.mgr.Keeper(), active)
		active = filterInsolventVaults(ctx, qs.mgr.Keeper(), active)
	}

	resp := make([]*types.QueryInboundAddressResponse, 0)
	constAccessor := qs.mgr.GetConstants()
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)

	k := qs.mgr.Keeper()
	if k == nil {
		ctx.Logger().Error("keeper is nil, can't fulfill query")
		return nil, errors.New("keeper is nil, can't fulfill query")
	}
	chains := make(common.Chains, 0)
	for _, vault := range active {
		chains = append(chains, vault.GetChains()...)
	}
	chains = chains.Distinct()
	if len(chains) == 0 {
		chains = common.Chains{common.RuneAsset().Chain}
	}

	isGlobalTradingPaused := k.IsGlobalTradingHalted(ctx)

	for _, chain := range chains {
		// tx send to thorchain doesn't need an address , thus here skip it
		if chain == common.THORChain {
			continue
		}

		isChainTradingPaused := k.IsChainTradingHalted(ctx, chain)
		isChainLpPaused := k.IsLPPaused(ctx, chain)

		vault, err := selectActiveVaultForChain(ctx, k, active, chain, signingTransactionPeriod)
		if err != nil {
			if chain == common.XMRChain {
				ctx.Logger().Info("skipping XMR inbound address until an active XMR-capable vault exists", "error", err)
				continue
			}
			ctx.Logger().Error("fail to select active vault for chain", "chain", chain.String(), "error", err)
			return nil, fmt.Errorf("fail to select active vault for chain %s: %w", chain, err)
		}
		vaultAddress, err := vault.GetAddress(chain)
		if err != nil {
			ctx.Logger().Error("fail to get address for chain", "error", err)
			return nil, fmt.Errorf("fail to get address for chain: %w", err)
		}
		cc := vault.GetContract(chain)
		gasRate := qs.mgr.GasMgr().GetGasRate(ctx, chain)
		networkFeeInfo, err := qs.mgr.GasMgr().GetNetworkFee(ctx, chain)
		if err != nil {
			ctx.Logger().Error("fail to get network fee info", "error", err)
			return nil, fmt.Errorf("fail to get network fee info: %w", err)
		}

		// Retrieve the outbound fee for the chain's gas asset - fee will be zero if no network fee has been posted/the pool doesn't exist
		outboundFee, _ := qs.mgr.GasMgr().GetAssetOutboundFee(ctx, chain.GetGasAsset(), false)

		gasUnits, _ := chain.GetGasUnits()
		pubKey, err := vault.AlgoPubKey(chain)
		if err != nil {
			ctx.Logger().Error("fail to get pubkey for chain", "error", err)
			return nil, fmt.Errorf("fail to get pubkey for chain: %w", err)
		}

		address := vaultAddress
		if chain == common.ZECChain {
			address, err = address.ToTexAddress()
			if err != nil {
				ctx.Logger().Error("fail convert address", "chain", chain.String(), "error", err)
			}
		}

		var viewKey string
		if chain == common.XMRChain {
			viewKey, _ = common.XMRViewKeyHex()
		}

		addr := types.QueryInboundAddressResponse{
			Chain:                chain.String(),
			PubKey:               pubKey.String(),
			Address:              address.String(),
			Router:               cc.Router.String(),
			Halted:               isGlobalTradingPaused || isChainTradingPaused,
			GlobalTradingPaused:  isGlobalTradingPaused,
			ChainTradingPaused:   isChainTradingPaused,
			ChainLpActionsPaused: isChainLpPaused,
			ObservedFeeRate:      cosmos.NewUint(networkFeeInfo.TransactionFeeRate).String(),
			GasRate:              gasRate.String(),
			GasRateUnits:         gasUnits,
			OutboundTxSize:       cosmos.NewUint(networkFeeInfo.TransactionSize).String(),
			OutboundFee:          outboundFee.String(),
			DustThreshold:        chain.DustThreshold().String(),
			ViewKey:              viewKey,
		}

		resp = append(resp, &addr)
	}

	return &types.QueryInboundAddressesResponse{
		InboundAddresses: resp,
	}, nil
}

// observeChainsFromHeights converts keeper observe heights into the query
// response type. No sorting is needed or performed here: the keeper returns the
// heights in ascending chain order, and this only maps them one-for-one.
func observeChainsFromHeights(chainHeights []kvTypes.ChainHeight) []*types.ChainHeight {
	observeChains := make([]*types.ChainHeight, 0, len(chainHeights))
	for _, chainHeight := range chainHeights {
		observeChains = append(observeChains, &types.ChainHeight{
			Chain:  chainHeight.Chain.String(),
			Height: chainHeight.Height,
		})
	}
	return observeChains
}

// queryNode return the Node information related to the request node address
// /thorchain/node/{nodeaddress}
func (qs queryServer) queryNode(ctx cosmos.Context, req *types.QueryNodeRequest) (*types.QueryNodeResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("node address not provided")
	}
	nodeAddress := req.Address
	addr, err := cosmos.AccAddressFromBech32(nodeAddress)
	if err != nil {
		return nil, cosmos.ErrUnknownRequest("invalid account address")
	}

	nodeAcc, err := qs.mgr.Keeper().GetNodeAccount(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}

	slashPts, err := qs.mgr.Keeper().GetNodeAccountSlashPoints(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fail to get node slash points: %w", err)
	}
	jail, err := qs.mgr.Keeper().GetNodeAccountJail(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to get node jail: %w", err)
	}

	bp, err := qs.mgr.Keeper().GetBondProviders(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return nil, fmt.Errorf("fail to get bond providers: %w", err)
	}
	bp.Adjust(nodeAcc.Bond)

	active, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get all active node account: %w", err)
	}

	result := types.QueryNodeResponse{
		NodeAddress: nodeAcc.NodeAddress.String(),
		Status:      nodeAcc.Status.String(),
		PubKeySet: common.PubKeySet{
			Secp256k1: common.PubKey(nodeAcc.PubKeySet.Secp256k1.String()),
			Ed25519:   common.PubKey(nodeAcc.PubKeySet.Ed25519.String()),
		},
		ValidatorConsPubKey: nodeAcc.ValidatorConsPubKey,
		ActiveBlockHeight:   nodeAcc.ActiveBlockHeight,
		StatusSince:         nodeAcc.StatusSince,
		NodeOperatorAddress: nodeAcc.BondAddress.String(),
		TotalBond:           nodeAcc.Bond.String(),
		SignerMembership:    nodeAcc.GetSignerMembership().Strings(),
		RequestedToLeave:    nodeAcc.RequestedToLeave,
		ForcedToLeave:       nodeAcc.ForcedToLeave,
		LeaveHeight:         int64(nodeAcc.LeaveScore), // OpenAPI can only represent uint64 as int64
		Maintenance:         nodeAcc.Maintenance,
		MissingBlocks:       int64(nodeAcc.MissingBlocks),
		IpAddress:           nodeAcc.IPAddress,
		Version:             nodeAcc.GetVersion().String(),
		CurrentAward:        cosmos.ZeroUint().String(), // Default display for if not overwritten.
	}
	result.PeerId = getPeerIDFromPubKey(nodeAcc.PubKeySet.Secp256k1)
	result.SlashPoints = slashPts

	result.Jail = &types.NodeJail{
		// Since redundant, leave out the node address
		ReleaseHeight: jail.ReleaseHeight,
		Reason:        jail.Reason,
	}

	var providers []*types.NodeBondProvider
	// Leave this nil (null rather than []) if the source is nil.
	if bp.Providers != nil {
		providers = make([]*types.NodeBondProvider, len(bp.Providers))
		for i, p := range bp.Providers {
			providers[i] = &types.NodeBondProvider{
				BondAddress:  p.BondAddress.String(),
				Bond:         p.Bond.String(),
				Bps:          p.Bps,
				PayoutTarget: p.PayoutTarget,
			}
		}
	}

	result.BondProviders = &types.NodeBondProviders{
		// Since redundant, leave out the node address
		NodeOperatorFee: bp.NodeOperatorFee.String(),
		Providers:       providers,
	}

	// CurrentAward is an estimation of reward for node in active status
	// Node in other status should not have current reward
	if nodeAcc.Status == NodeActive && !nodeAcc.Bond.IsZero() {
		var network Network
		network, err = qs.mgr.Keeper().GetNetwork(ctx)
		if err != nil {
			return nil, fmt.Errorf("fail to get network: %w", err)
		}
		var vaults []Vault
		vaults, err = qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return nil, fmt.Errorf("fail to get active vaults: %w", err)
		}
		if len(vaults) == 0 {
			return nil, fmt.Errorf("no active vaults")
		}

		totalEffectiveBond, bondHardCap := getTotalEffectiveBond(active)

		lastChurnHeight := vaults[0].StatusSince

		var reward cosmos.Uint
		reward, err = getNodeCurrentRewards(ctx, qs.mgr, nodeAcc, lastChurnHeight, network.BondRewardRune, totalEffectiveBond, bondHardCap)
		if err != nil {
			return nil, fmt.Errorf("fail to get current node rewards: %w", err)
		}

		result.CurrentAward = reward.String()
	}

	chainHeights, err := qs.mgr.Keeper().GetLastObserveHeight(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fail to get last observe chain height: %w", err)
	}
	result.ObserveChains = observeChainsFromHeights(chainHeights)

	preflightCheckResult, err := getNodePreflightResult(ctx, qs.mgr, nodeAcc)
	if err != nil {
		ctx.Logger().Error("fail to get node preflight result", "error", err)
	} else {
		result.PreflightStatus = &preflightCheckResult
	}
	return &result, nil
}

func getNodePreflightResult(ctx cosmos.Context, mgr *Mgrs, nodeAcc NodeAccount) (types.NodePreflightStatus, error) {
	constAccessor := mgr.GetConstants()
	preflightResult := types.NodePreflightStatus{}
	status, err := mgr.ValidatorMgr().NodeAccountPreflightCheck(ctx, nodeAcc, constAccessor)
	preflightResult.Status = status.String()
	if err != nil {
		preflightResult.Reason = err.Error()
		preflightResult.Code = 1
	} else {
		preflightResult.Reason = "OK"
		preflightResult.Code = 0
	}
	return preflightResult, nil
}

// Estimates current rewards for the NodeAccount taking into account bond-weighted rewards and slash points
func getNodeCurrentRewards(ctx cosmos.Context, mgr *Mgrs, nodeAcc NodeAccount, lastChurnHeight int64, totalBondReward, totalEffectiveBond, bondHardCap cosmos.Uint) (cosmos.Uint, error) {
	slashPts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get node slash points: %w", err)
	}

	// Find number of blocks since the last churn (the last bond reward payout)
	totalActiveBlocks := ctx.BlockHeight() - lastChurnHeight

	// find number of blocks they were well behaved (ie active - slash points)
	earnedBlocks := totalActiveBlocks - slashPts
	if earnedBlocks < 0 {
		earnedBlocks = 0
	}

	naEffectiveBond := nodeAcc.Bond
	if naEffectiveBond.GT(bondHardCap) {
		naEffectiveBond = bondHardCap
	}

	// reward = totalBondReward * (naEffectiveBond / totalEffectiveBond) * (unslashed blocks since last churn / blocks since last churn)
	reward := common.GetUncappedShare(naEffectiveBond, totalEffectiveBond, totalBondReward)
	reward = common.GetUncappedShare(cosmos.NewUint(uint64(earnedBlocks)), cosmos.NewUint(uint64(totalActiveBlocks)), reward)
	return reward, nil
}

// queryNodes return all the nodes that has bond
// /thorchain/nodes
func (qs queryServer) queryNodes(ctx cosmos.Context, _ *types.QueryNodesRequest) (*types.QueryNodesResponse, error) {
	nodeAccounts, err := qs.mgr.Keeper().ListValidatorsWithBond(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}

	active, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get all active node account: %w", err)
	}

	network, err := qs.mgr.Keeper().GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get network: %w", err)
	}

	vaults, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return nil, fmt.Errorf("fail to get active vaults: %w", err)
	}
	if len(vaults) == 0 {
		return nil, fmt.Errorf("no active vaults")
	}

	totalEffectiveBond, bondHardCap := getTotalEffectiveBond(active)

	lastChurnHeight := vaults[0].StatusSince
	result := make([]*types.QueryNodeResponse, len(nodeAccounts))
	for i, na := range nodeAccounts {
		if na.RequestedToLeave && na.Bond.LTE(cosmos.NewUint(common.One)) {
			// ignore the node , it left and also has very little bond
			// Set the default display for fields which would otherwise be "".
			result[i] = &types.QueryNodeResponse{
				Status:          types.NodeStatus_Unknown.String(),
				TotalBond:       cosmos.ZeroUint().String(),
				BondProviders:   &types.NodeBondProviders{NodeOperatorFee: cosmos.ZeroUint().String()},
				Version:         semver.MustParse("0.0.0").String(),
				CurrentAward:    cosmos.ZeroUint().String(),
				PreflightStatus: &types.NodePreflightStatus{Status: types.NodeStatus_Unknown.String()},
			}
			continue
		}

		slashPts, err := qs.mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to get node slash points: %w", err)
		}

		result[i] = &types.QueryNodeResponse{
			NodeAddress: na.NodeAddress.String(),
			Status:      na.Status.String(),
			PubKeySet: common.PubKeySet{
				Secp256k1: common.PubKey(na.PubKeySet.Secp256k1.String()),
				Ed25519:   common.PubKey(na.PubKeySet.Ed25519.String()),
			},
			ValidatorConsPubKey: na.ValidatorConsPubKey,
			ActiveBlockHeight:   na.ActiveBlockHeight,
			StatusSince:         na.StatusSince,
			NodeOperatorAddress: na.BondAddress.String(),
			TotalBond:           na.Bond.String(),
			SignerMembership:    na.GetSignerMembership().Strings(),
			RequestedToLeave:    na.RequestedToLeave,
			ForcedToLeave:       na.ForcedToLeave,
			LeaveHeight:         int64(na.LeaveScore), // OpenAPI can only represent uint64 as int64
			Maintenance:         na.Maintenance,
			MissingBlocks:       int64(na.MissingBlocks),
			IpAddress:           na.IPAddress,
			Version:             na.GetVersion().String(),
			CurrentAward:        cosmos.ZeroUint().String(), // Default display for if not overwritten.
		}
		result[i].PeerId = getPeerIDFromPubKey(na.PubKeySet.Secp256k1)
		result[i].SlashPoints = slashPts
		if na.Status == NodeActive {
			var reward cosmos.Uint
			reward, err = getNodeCurrentRewards(ctx, qs.mgr, na, lastChurnHeight, network.BondRewardRune, totalEffectiveBond, bondHardCap)
			if err != nil {
				return nil, fmt.Errorf("fail to get current node rewards: %w", err)
			}

			result[i].CurrentAward = reward.String()
		}

		var jail Jail
		jail, err = qs.mgr.Keeper().GetNodeAccountJail(ctx, na.NodeAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to get node jail: %w", err)
		}
		result[i].Jail = &types.NodeJail{
			// Since redundant, leave out the node address
			ReleaseHeight: jail.ReleaseHeight,
			Reason:        jail.Reason,
		}

		chainHeights, err := qs.mgr.Keeper().GetLastObserveHeight(ctx, na.NodeAddress)
		if err != nil {
			return nil, fmt.Errorf("fail to get last observe chain height: %w", err)
		}
		result[i].ObserveChains = observeChainsFromHeights(chainHeights)

		preflightCheckResult, err := getNodePreflightResult(ctx, qs.mgr, na)
		if err != nil {
			ctx.Logger().Error("fail to get node preflight result", "error", err)
		} else {
			result[i].PreflightStatus = &preflightCheckResult
		}

		bp, err := qs.mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
		if err != nil {
			ctx.Logger().Error("fail to get bond providers", "error", err)
		}
		bp.Adjust(na.Bond)

		var providers []*types.NodeBondProvider
		// Leave this nil (null rather than []) if the source is nil.
		if bp.Providers != nil {
			providers = make([]*types.NodeBondProvider, len(bp.Providers))
			for i := range bp.Providers {
				providers[i] = &types.NodeBondProvider{
					BondAddress:  bp.Providers[i].BondAddress.String(),
					Bond:         bp.Providers[i].Bond.String(),
					Bps:          bp.Providers[i].Bps,
					PayoutTarget: bp.Providers[i].PayoutTarget,
				}
			}
		}

		result[i].BondProviders = &types.NodeBondProviders{
			// Since redundant, leave out the node address
			NodeOperatorFee: bp.NodeOperatorFee.String(),
			Providers:       providers,
		}
	}

	return &types.QueryNodesResponse{Nodes: result}, nil
}

func newSaver(lp LiquidityProvider, pool Pool) *types.QuerySaverResponse {
	assetRedeemableValue := lp.GetSaversAssetRedeemValue(pool)

	gp := cosmos.NewDec(0)
	if !lp.AssetDepositValue.IsZero() {
		adv := cosmos.NewDec(lp.AssetDepositValue.BigInt().Int64())
		arv := cosmos.NewDec(assetRedeemableValue.BigInt().Int64())
		gp = arv.Sub(adv)
		gp = gp.Quo(adv)
	}

	return &types.QuerySaverResponse{
		Asset:              lp.Asset.GetLayer1Asset().String(),
		AssetAddress:       lp.AssetAddress.String(),
		LastAddHeight:      lp.LastAddHeight,
		LastWithdrawHeight: lp.LastWithdrawHeight,
		Units:              lp.Units.String(),
		AssetDepositValue:  lp.AssetDepositValue.String(),
		AssetRedeemValue:   assetRedeemableValue.String(),
		GrowthPct:          gp.String(),
	}
}

// queryLiquidityProviders
func (qs queryServer) queryLiquidityProviders(ctx cosmos.Context, req *types.QueryLiquidityProvidersRequest) (*types.QueryLiquidityProvidersResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}
	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}
	if asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is a SaversPool")
	}

	var lps []*types.QueryLiquidityProviderResponse
	iterator := qs.mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &lp)
		lps = append(lps, &types.QueryLiquidityProviderResponse{
			// No redeem or LUVI calculations for the array response.
			Asset:              lp.Asset.GetLayer1Asset().String(),
			RuneAddress:        lp.RuneAddress.String(),
			AssetAddress:       lp.AssetAddress.String(),
			LastAddHeight:      lp.LastAddHeight,
			LastWithdrawHeight: lp.LastWithdrawHeight,
			Units:              lp.Units.String(),
			PendingRune:        lp.PendingRune.String(),
			PendingAsset:       lp.PendingAsset.String(),
			PendingTxId:        lp.PendingTxID.String(),
			RuneDepositValue:   lp.RuneDepositValue.String(),
			AssetDepositValue:  lp.AssetDepositValue.String(),
		})
	}
	return &types.QueryLiquidityProvidersResponse{LiquidityProviders: lps}, nil
}

// queryLiquidityProvider
func (qs queryServer) queryLiquidityProvider(ctx cosmos.Context, req *types.QueryLiquidityProviderRequest) (*types.QueryLiquidityProviderResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	if len(req.Address) == 0 {
		return nil, errors.New("lp not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}

	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}

	if asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is a SaversPool")
	}

	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	lp, err := qs.mgr.Keeper().GetLiquidityProvider(ctx, asset, addr)
	if err != nil {
		ctx.Logger().Error("fail to get liquidity provider", "error", err)
		return nil, fmt.Errorf("fail to liquidity provider: %w", err)
	}

	poolAsset := asset

	pool, err := qs.mgr.Keeper().GetPool(ctx, poolAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("fail to get pool: %w", err)
	}

	synthSupply := qs.mgr.Keeper().GetTotalSupply(ctx, poolAsset.GetSyntheticAsset())
	_, runeRedeemValue := lp.GetRuneRedeemValue(pool, synthSupply)
	_, assetRedeemValue := lp.GetAssetRedeemValue(pool, synthSupply)
	_, luviDepositValue := lp.GetLuviDepositValue(pool)
	_, luviRedeemValue := lp.GetLuviRedeemValue(runeRedeemValue, assetRedeemValue)

	lgp := cosmos.NewDec(0)
	if !luviDepositValue.IsZero() {
		ldv := cosmos.NewDec(luviDepositValue.BigInt().Int64())
		lrv := cosmos.NewDec(luviRedeemValue.BigInt().Int64())
		lgp = lrv.Sub(ldv)
		lgp = lgp.Quo(ldv)
	}

	liqp := types.QueryLiquidityProviderResponse{
		Asset:              lp.Asset.GetLayer1Asset().String(),
		RuneAddress:        lp.RuneAddress.String(),
		AssetAddress:       lp.AssetAddress.String(),
		LastAddHeight:      lp.LastAddHeight,
		LastWithdrawHeight: lp.LastWithdrawHeight,
		Units:              lp.Units.String(),
		PendingRune:        lp.PendingRune.String(),
		PendingAsset:       lp.PendingAsset.String(),
		PendingTxId:        lp.PendingTxID.String(),
		RuneDepositValue:   lp.RuneDepositValue.String(),
		AssetDepositValue:  lp.AssetDepositValue.String(),
		RuneRedeemValue:    runeRedeemValue.String(),
		AssetRedeemValue:   assetRedeemValue.String(),
		LuviDepositValue:   luviDepositValue.String(),
		LuviRedeemValue:    luviRedeemValue.String(),
		LuviGrowthPct:      lgp.String(),
	}

	return &liqp, nil
}

// querySavers
func (qs queryServer) querySavers(ctx cosmos.Context, req *types.QuerySaversRequest) (*types.QuerySaversResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	req.Asset = strings.Replace(req.Asset, ".", "/", 1)
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}
	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}
	if !asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is not a SaversPool")
	}

	poolAsset := asset.GetSyntheticAsset()

	pool, err := qs.mgr.Keeper().GetPool(ctx, poolAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("fail to get pool: %w", err)
	}

	var savers []*types.QuerySaverResponse
	iterator := qs.mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &lp)
		savers = append(savers, newSaver(lp, pool))
	}

	return &types.QuerySaversResponse{Savers: savers}, nil
}

// querySaver
// isSavers is true if request is for the savers of a Savers Pool, if false the request is for an L1 pool
func (qs queryServer) querySaver(ctx cosmos.Context, req *types.QuerySaverRequest) (*types.QuerySaverResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	if len(req.Address) == 0 {
		return nil, errors.New("lp not provided")
	}
	req.Asset = strings.Replace(req.Asset, ".", "/", 1)
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to get parse asset", "error", err)
		return nil, fmt.Errorf("fail to parse asset: %w", err)
	}

	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("must not be a derived asset")
	}

	if !asset.IsSyntheticAsset() {
		return nil, fmt.Errorf("invalid request: requested pool is not a SaversPool")
	}

	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	lp, err := qs.mgr.Keeper().GetLiquidityProvider(ctx, asset, addr)
	if err != nil {
		ctx.Logger().Error("fail to get liquidity provider", "error", err)
		return nil, fmt.Errorf("fail to liquidity provider: %w", err)
	}

	poolAsset := asset.GetSyntheticAsset()

	pool, err := qs.mgr.Keeper().GetPool(ctx, poolAsset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("fail to get pool: %w", err)
	}

	saver := newSaver(lp, pool)

	return saver, nil
}

func newStreamingSwap(streamingSwap StreamingSwap, msgSwap MsgSwap) *types.QueryStreamingSwapResponse {
	var sourceAsset common.Asset
	// Leave the source_asset field empty if there is more than a single input Coin.
	if len(msgSwap.Tx.Coins) == 1 {
		sourceAsset = msgSwap.Tx.Coins[0].Asset
	}

	var failedSwaps []int64
	// Leave this nil (null rather than []) if the source is nil.
	if streamingSwap.FailedSwaps != nil {
		failedSwaps = make([]int64, len(streamingSwap.FailedSwaps))
		for i := range streamingSwap.FailedSwaps {
			failedSwaps[i] = int64(streamingSwap.FailedSwaps[i])
		}
	}

	return &types.QueryStreamingSwapResponse{
		TxId:              streamingSwap.TxID.String(),
		Interval:          int64(streamingSwap.Interval),
		Quantity:          int64(streamingSwap.Quantity),
		Count:             int64(streamingSwap.Count),
		LastHeight:        streamingSwap.LastHeight,
		InitialHeight:     msgSwap.InitialBlockHeight,
		TradeTarget:       streamingSwap.TradeTarget.String(),
		SourceAsset:       sourceAsset.String(),
		TargetAsset:       msgSwap.TargetAsset.String(),
		Destination:       msgSwap.Destination.String(),
		Deposit:           streamingSwap.Deposit.String(),
		In:                streamingSwap.In.String(),
		Out:               streamingSwap.Out.String(),
		FailedSwaps:       failedSwaps,
		FailedSwapReasons: streamingSwap.FailedSwapReasons,
	}
}

// newStreamingSwapFromAdvQueue converts an advanced swap queue MsgSwap to QueryStreamingSwapResponse format
func newStreamingSwapFromAdvQueue(msgSwap MsgSwap) *types.QueryStreamingSwapResponse {
	var sourceAsset common.Asset
	// Leave the source_asset field empty if there is more than a single input Coin.
	if len(msgSwap.Tx.Coins) == 1 {
		sourceAsset = msgSwap.Tx.Coins[0].Asset
	}

	var failedSwaps []int64
	// Leave this nil (null rather than []) if the source is nil.
	if msgSwap.State.FailedSwaps != nil {
		failedSwaps = make([]int64, len(msgSwap.State.FailedSwaps))
		for i := range msgSwap.State.FailedSwaps {
			failedSwaps[i] = int64(msgSwap.State.FailedSwaps[i])
		}
	}

	return &types.QueryStreamingSwapResponse{
		TxId:              msgSwap.Tx.ID.String(),
		Interval:          int64(msgSwap.State.Interval),
		Quantity:          int64(msgSwap.State.Quantity),
		Count:             int64(msgSwap.State.Count),
		LastHeight:        msgSwap.State.LastHeight,
		InitialHeight:     msgSwap.InitialBlockHeight,
		TradeTarget:       msgSwap.TradeTarget.String(),
		SourceAsset:       sourceAsset.String(),
		TargetAsset:       msgSwap.TargetAsset.String(),
		Destination:       msgSwap.Destination.String(),
		Deposit:           msgSwap.State.Deposit.String(),
		In:                msgSwap.State.In.String(),
		Out:               msgSwap.State.Out.String(),
		FailedSwaps:       failedSwaps,
		FailedSwapReasons: msgSwap.State.FailedSwapReasons,
	}
}

func (qs queryServer) queryStreamingSwaps(ctx cosmos.Context, _ *types.QueryStreamingSwapsRequest) (*types.QueryStreamingSwapsResponse, error) {
	var streams []*types.QueryStreamingSwapResponse

	// Get legacy streaming swaps
	iter := qs.mgr.Keeper().GetStreamingSwapIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var stream StreamingSwap
		qs.mgr.Keeper().Cdc().MustUnmarshal(iter.Value(), &stream)

		var msgSwap MsgSwap
		// Check up to the first two indices (0 through 1) for the MsgSwap; if not found, leave the fields blank.
		for i := 0; i <= 1; i++ {
			swapQueueItem, err := qs.mgr.Keeper().GetSwapQueueItem(ctx, stream.TxID, i)
			if err != nil {
				// GetSwapQueueItem returns an error if there is no MsgSwap set for that index, a normal occurrence here.
				continue
			}
			if !swapQueueItem.IsLegacyStreaming() {
				continue
			}
			// In case there are multiple streaming swaps with the same TxID, check the input amount.
			if len(swapQueueItem.Tx.Coins) == 0 || !swapQueueItem.Tx.Coins[0].Amount.Equal(stream.Deposit) {
				continue
			}
			msgSwap = swapQueueItem
			break
		}

		streams = append(streams, newStreamingSwap(stream, msgSwap))
	}

	// Get advanced swap queue streaming swaps
	advIter := qs.mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer advIter.Close()
	for ; advIter.Valid(); advIter.Next() {
		var msgSwap MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(advIter.Value(), &msgSwap); err != nil {
			ctx.Logger().Error("failed to unmarshal advanced swap queue item", "error", err)
			continue
		}

		// Only include streaming swaps (quantity > 1)
		if !msgSwap.IsStreaming() {
			continue
		}

		// Convert advanced swap queue MsgSwap to streaming swap response format
		streams = append(streams, newStreamingSwapFromAdvQueue(msgSwap))
	}

	return &types.QueryStreamingSwapsResponse{StreamingSwaps: streams}, nil
}

func (qs queryServer) querySwapperClout(ctx cosmos.Context, req *types.QuerySwapperCloutRequest) (*types.SwapperClout, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse address", "error", err)
		return nil, fmt.Errorf("could not parse address: %w", err)
	}

	clout, err := qs.mgr.Keeper().GetSwapperClout(ctx, addr)
	if err != nil {
		ctx.Logger().Error("fail to get swapper clout", "error", err)
		return nil, fmt.Errorf("could not get swapper clout: %w", err)
	}

	return &clout, nil
}

func (qs queryServer) queryStreamingSwap(ctx cosmos.Context, req *types.QueryStreamingSwapRequest) (*types.QueryStreamingSwapResponse, error) {
	if len(req.TxId) == 0 {
		return nil, errors.New("tx id not provided")
	}
	txid, err := common.NewTxID(req.TxId)
	if err != nil {
		ctx.Logger().Error("fail to parse txid", "error", err)
		return nil, fmt.Errorf("could not parse txid: %w", err)
	}

	// First try advanced swap queue (primary system since EnableAdvSwapQueue = 1 by default)
	// Check up to the first two indices (0 through 1) for streaming swaps in advanced queue
	for i := 0; i <= 1; i++ {
		var advSwapItem MsgSwap
		advSwapItem, err = qs.mgr.Keeper().GetAdvSwapQueueItem(ctx, txid, i)
		if err != nil {
			// GetAdvSwapQueueItem returns an error if there is no MsgSwap set for that index, a normal occurrence here.
			continue
		}
		if !advSwapItem.IsStreaming() {
			continue
		}
		// Found streaming swap in advanced queue
		result := newStreamingSwapFromAdvQueue(advSwapItem)
		return result, nil
	}

	// Advanced swap queue not found, try legacy streaming swap for backward compatibility
	streamingSwap, err := qs.mgr.Keeper().GetStreamingSwap(ctx, txid)
	if err == nil {
		// Found legacy streaming swap, look for corresponding MsgSwap
		var msgSwap MsgSwap
		// Check up to the first two indices (0 through 1) for the MsgSwap; if not found, leave the fields blank.
		for i := 0; i <= 1; i++ {
			swapQueueItem, err := qs.mgr.Keeper().GetSwapQueueItem(ctx, txid, i)
			if err != nil {
				// GetSwapQueueItem returns an error if there is no MsgSwap set for that index, a normal occurrence here.
				continue
			}
			if !swapQueueItem.IsLegacyStreaming() {
				continue
			}
			// In case there are multiple streaming swaps with the same TxID, check the input amount.
			if len(swapQueueItem.Tx.Coins) == 0 || !swapQueueItem.Tx.Coins[0].Amount.Equal(streamingSwap.Deposit) {
				continue
			}
			msgSwap = swapQueueItem
			break
		}

		result := newStreamingSwap(streamingSwap, msgSwap)
		return result, nil
	}

	// Neither advanced queue nor legacy system contains the streaming swap
	ctx.Logger().Error("streaming swap not found in advanced queue or legacy system", "txid", txid)
	return nil, fmt.Errorf("could not find streaming swap: %s", txid)
}

func (qs queryServer) queryPool(ctx cosmos.Context, req *types.QueryPoolRequest) (*types.QueryPoolResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	if asset.IsDerivedAsset() {
		return nil, fmt.Errorf("asset: %s is a derived asset", req.Asset)
	}

	pool, err := qs.mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		ctx.Logger().Error("fail to get pool", "error", err)
		return nil, fmt.Errorf("could not get pool: %w", err)
	}
	if pool.IsEmpty() {
		return nil, fmt.Errorf("pool: %s doesn't exist", req.Asset)
	}

	// Get Savers Vault for this L1 pool if it's a gas asset
	saversAsset := pool.Asset.GetSyntheticAsset()
	saversPool, err := qs.mgr.Keeper().GetPool(ctx, saversAsset)
	if err != nil {
		return nil, fmt.Errorf("fail to unmarshal savers vault: %w", err)
	}

	saversDepth := saversPool.BalanceAsset
	saversUnits := saversPool.LPUnits
	synthSupply := qs.mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	pool.CalcUnits(synthSupply)

	synthMintPausedErr := isSynthMintPaused(ctx, qs.mgr, saversAsset, cosmos.ZeroUint())
	synthSupplyRemaining, _ := getSynthSupplyRemaining(ctx, qs.mgr, saversAsset)

	maxSynthsForSaversYield := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxSynthsForSaversYield)
	// Capping the synths at double the pool balance of Assets.
	maxSynthsForSaversYieldUint := common.GetUncappedShare(cosmos.NewUint(uint64(maxSynthsForSaversYield)), cosmos.NewUint(constants.MaxBasisPts), pool.BalanceAsset.MulUint64(2))

	saversFillBps := common.GetUncappedShare(synthSupply, maxSynthsForSaversYieldUint, cosmos.NewUint(constants.MaxBasisPts))
	saversCapacityRemaining := common.SafeSub(maxSynthsForSaversYieldUint, synthSupply)
	runeDepth, _, _ := qs.mgr.NetworkMgr().CalcAnchor(ctx, qs.mgr, asset)
	dpool, _ := qs.mgr.Keeper().GetPool(ctx, asset.GetDerivedAsset())
	dbps := common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
	if dpool.Status != PoolAvailable {
		dbps = cosmos.ZeroUint()
	}

	tradingHalted := qs.mgr.Keeper().IsGlobalTradingHalted(ctx)

	l1Asset := pool.Asset.GetLayer1Asset()
	chain := l1Asset.GetChain()

	if !pool.IsAvailable() {
		tradingHalted = true
	}

	if !tradingHalted && qs.mgr.Keeper().IsChainTradingHalted(ctx, chain) {
		tradingHalted = true
	}

	if !tradingHalted && qs.mgr.Keeper().IsChainHalted(ctx, chain) {
		tradingHalted = true
	}

	if !tradingHalted && qs.mgr.Keeper().IsRagnarok(ctx, []common.Asset{l1Asset}) {
		tradingHalted = true
	}

	volume, err := qs.mgr.Keeper().GetVolume(ctx, pool.Asset)
	if err != nil {
		// fallback to display "0" volume
		volume = types.NewVolume(pool.Asset)
	}

	p := types.QueryPoolResponse{
		Asset:               pool.Asset.String(),
		ShortCode:           pool.Asset.ShortCode(),
		Status:              pool.Status.String(),
		Decimals:            pool.Decimals,
		PendingInboundAsset: pool.PendingInboundAsset.String(),
		PendingInboundRune:  pool.PendingInboundRune.String(),
		BalanceAsset:        pool.BalanceAsset.String(),
		BalanceRune:         pool.BalanceRune.String(),
		PoolUnits:           pool.GetPoolUnits().String(),
		LPUnits:             pool.LPUnits.String(),
		SynthUnits:          pool.SynthUnits.String(),
		TradingHalted:       tradingHalted,
		VolumeRune:          volume.TotalRune.String(),
		VolumeAsset:         volume.TotalAsset.String(),
	}
	p.SynthSupply = synthSupply.String()
	p.SaversDepth = saversDepth.String()
	p.SaversUnits = saversUnits.String()
	p.SaversFillBps = saversFillBps.String()
	p.SaversCapacityRemaining = saversCapacityRemaining.String()
	p.SynthMintPaused = (synthMintPausedErr != nil)
	p.SynthSupplyRemaining = synthSupplyRemaining.String()
	p.DerivedDepthBps = dbps.String()
	setPoolLuviFields(&p, pool, synthSupply)
	setPoolLuviAccumulatorFields(ctx, qs.mgr.Keeper(), &p, pool)
	if !pool.Asset.IsSyntheticAsset() {
		setPoolFairValueFields(&p, pool, synthSupply,
			oraclePriceUSD(ctx, qs.mgr.Keeper(), pool.Asset.Ticker.String()),
			oraclePriceUSD(ctx, qs.mgr.Keeper(), common.RuneAsset().Ticker.String()))
	}

	polReserveDeposit, err := qs.mgr.Keeper().GetPOLReserveDeposit(ctx, pool.Asset)
	if err == nil {
		p.PolReserveRuneDeposited = polReserveDeposit.RuneDeposited.String()
	}

	rollingFee, err := qs.mgr.Keeper().GetRollingPoolLiquidityFee(ctx, pool.Asset)
	if err == nil {
		p.RollingPoolLiquidityFeeRune = cosmos.NewUint(rollingFee).String()
	}

	if !pool.BalanceAsset.IsZero() && !pool.BalanceRune.IsZero() {
		dollarsPerRune := dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper())
		p.AssetTorPrice = dollarsPerRune.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).String()
	}

	return &p, nil
}

// setPoolLuviFields fills the stateless LUVI fields of a pool response: the
// classic index as the chain values a pool unit, the LP equity net of the
// synth liability, and the synth overhang indicators. See types/type_luvi.go.
func setPoolLuviFields(p *types.QueryPoolResponse, pool Pool, synthSupply cosmos.Uint) {
	st := types.NewLuviState(pool, synthSupply)
	p.Luvi = st.ChainLuvi().String()
	p.LpEquity = st.LPEquity().String()
	p.LpEquityPerUnit = st.LPEquityPerUnit().String()
	p.SynthPerPoolDepthBps = st.SynthPerPoolDepthBps().String()
	// an empty pool (no asset depth, no synths) is not in overhang, but a
	// depleted one (no asset depth, synths outstanding) is
	p.SynthOverhang = !st.SynthSupply.IsZero() && st.IsOverhang()
}

// oraclePriceUSD returns the oracle USD price of symbol, or "" when the
// oracle has none. The price store is wiped every BeginBlock and refilled only
// with the symbols that reached quorum in that block, so a price that exists
// is the current block's.
func oraclePriceUSD(ctx cosmos.Context, k keeper.Keeper, symbol string) string {
	price, err := k.GetPrice(ctx, symbol)
	if err != nil {
		return ""
	}
	return price.Price
}

// setPoolFairValueFields fills the oracle-priced (fair) LP unit value fields of
// a pool response. They are left unset unless both USD prices are present and
// valid, and the pool has depth and LP units. See types/type_luvi_fair.go.
func setPoolFairValueFields(p *types.QueryPoolResponse, pool Pool, synthSupply cosmos.Uint, assetUSD, runeUSD string) {
	if assetUSD == "" || runeUSD == "" {
		return
	}
	price, err := types.NewLuviPriceFromUSD(assetUSD, runeUSD)
	if err != nil {
		return
	}
	fv, err := types.NewLuviState(pool, synthSupply).FairValue(price)
	if err != nil {
		return
	}
	p.LpUnitFairValueRune = fv.UnitFairValueRune.String()
	p.LpUnitFairValueAsset = fv.UnitFairValueAsset.String()
	p.LpUnitSpotValueRune = fv.UnitSpotValueRune.String()
	p.FairValueOraclePriceRune = fv.OraclePriceRune.String()
	p.LpFairValueOverhang = fv.Overhang
	if fv.HasDeviation {
		p.LpFairSpotDeviationBps = fv.DeviationBps.String()
	}
}

func (qs queryServer) queryPools(ctx cosmos.Context, _ *types.QueryPoolsRequest) (*types.QueryPoolsResponse, error) {
	dollarsPerRune := dollarsPerRuneIgnoreHalt(ctx, qs.mgr.Keeper())
	runeUSD := oraclePriceUSD(ctx, qs.mgr.Keeper(), common.RuneAsset().Ticker.String())

	isGlobalTradingHalted := qs.mgr.Keeper().IsGlobalTradingHalted(ctx)
	isChainOrChainTradingHalted := map[common.Chain]bool{}

	pools := make([]*types.QueryPoolResponse, 0)
	iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
		}
		// ignore pool if no liquidity provider units
		if pool.LPUnits.IsZero() {
			continue
		}

		// Ignore synth asset pool (savers). Info will be on the L1 pool
		if pool.Asset.IsSyntheticAsset() {
			continue
		}

		// Ignore derived assets (except TOR)
		if pool.Asset.IsDerivedAsset() {
			continue
		}

		// Get Savers Vault
		saversAsset := pool.Asset.GetSyntheticAsset()
		saversPool, err := qs.mgr.Keeper().GetPool(ctx, saversAsset)
		if err != nil {
			return nil, fmt.Errorf("fail to unmarshal savers vault: %w", err)
		}

		saversDepth := saversPool.BalanceAsset
		saversUnits := saversPool.LPUnits

		synthSupply := qs.mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
		pool.CalcUnits(synthSupply)

		synthMintPausedErr := isSynthMintPaused(ctx, qs.mgr, pool.Asset, cosmos.ZeroUint())
		synthSupplyRemaining, _ := getSynthSupplyRemaining(ctx, qs.mgr, pool.Asset)

		maxSynthsForSaversYield := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxSynthsForSaversYield)
		// Capping the synths at double the pool balance of Assets.
		maxSynthsForSaversYieldUint := common.GetUncappedShare(cosmos.NewUint(uint64(maxSynthsForSaversYield)), cosmos.NewUint(constants.MaxBasisPts), pool.BalanceAsset.MulUint64(2))

		saversFillBps := common.GetUncappedShare(synthSupply, maxSynthsForSaversYieldUint, cosmos.NewUint(constants.MaxBasisPts))
		saversCapacityRemaining := common.SafeSub(maxSynthsForSaversYieldUint, synthSupply)
		runeDepth, _, _ := qs.mgr.NetworkMgr().CalcAnchor(ctx, qs.mgr, pool.Asset)
		dpool, _ := qs.mgr.Keeper().GetPool(ctx, pool.Asset.GetDerivedAsset())
		dbps := common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
		if dpool.Status != PoolAvailable {
			dbps = cosmos.ZeroUint()
		}

		tradingHalted := isGlobalTradingHalted

		l1Asset := pool.Asset.GetLayer1Asset()
		chain := l1Asset.GetChain()

		_, found := isChainOrChainTradingHalted[chain]
		if !found {
			isChainHalted := qs.mgr.Keeper().IsChainHalted(ctx, chain)
			isChainTradingHalted := qs.mgr.Keeper().IsChainTradingHalted(ctx, chain)

			isChainOrChainTradingHalted[chain] = isChainHalted || isChainTradingHalted
		}

		if !tradingHalted {
			tradingHalted = isChainOrChainTradingHalted[chain]
		}

		if !pool.IsAvailable() {
			tradingHalted = true
		}

		if qs.mgr.Keeper().IsRagnarok(ctx, []common.Asset{l1Asset}) {
			tradingHalted = true
		}

		volume, err := qs.mgr.Keeper().GetVolume(ctx, pool.Asset)
		if err != nil {
			// fallback to display "0" volume
			volume = types.NewVolume(pool.Asset)
		}

		p := types.QueryPoolResponse{
			Asset:               pool.Asset.String(),
			ShortCode:           pool.Asset.ShortCode(),
			Status:              pool.Status.String(),
			Decimals:            pool.Decimals,
			PendingInboundAsset: pool.PendingInboundAsset.String(),
			PendingInboundRune:  pool.PendingInboundRune.String(),
			BalanceAsset:        pool.BalanceAsset.String(),
			BalanceRune:         pool.BalanceRune.String(),
			PoolUnits:           pool.GetPoolUnits().String(),
			LPUnits:             pool.LPUnits.String(),
			SynthUnits:          pool.SynthUnits.String(),
			TradingHalted:       tradingHalted,
			VolumeRune:          volume.TotalRune.String(),
			VolumeAsset:         volume.TotalAsset.String(),
		}

		p.SynthSupply = synthSupply.String()
		p.SaversDepth = saversDepth.String()
		p.SaversUnits = saversUnits.String()
		p.SaversFillBps = saversFillBps.String()
		p.SaversCapacityRemaining = saversCapacityRemaining.String()
		p.SynthMintPaused = (synthMintPausedErr != nil)
		p.SynthSupplyRemaining = synthSupplyRemaining.String()
		p.DerivedDepthBps = dbps.String()
		setPoolLuviFields(&p, pool, synthSupply)
		setPoolLuviAccumulatorFields(ctx, qs.mgr.Keeper(), &p, pool)
		setPoolFairValueFields(&p, pool, synthSupply, oraclePriceUSD(ctx, qs.mgr.Keeper(), pool.Asset.Ticker.String()), runeUSD)

		polReserveDeposit, polErr := qs.mgr.Keeper().GetPOLReserveDeposit(ctx, pool.Asset)
		if polErr == nil {
			p.PolReserveRuneDeposited = polReserveDeposit.RuneDeposited.String()
		}

		rollingFee, rollingErr := qs.mgr.Keeper().GetRollingPoolLiquidityFee(ctx, pool.Asset)
		if rollingErr == nil {
			p.RollingPoolLiquidityFeeRune = cosmos.NewUint(rollingFee).String()
		}

		if !pool.BalanceAsset.IsZero() && !pool.BalanceRune.IsZero() {
			p.AssetTorPrice = dollarsPerRune.Mul(pool.BalanceRune).Quo(pool.BalanceAsset).String()
		}

		pools = append(pools, &p)
	}
	return &types.QueryPoolsResponse{Pools: pools}, nil
}

func (qs queryServer) queryPoolSlips(ctx cosmos.Context, asset string) (*types.QueryPoolSlipsResponse, error) {
	var assets []common.Asset
	if len(asset) > 0 {
		assetObj, err := common.NewAsset(asset)
		if err != nil {
			ctx.Logger().Error("fail to parse asset", "error", err, "asset", asset)
			return nil, fmt.Errorf("fail to parse asset (%s): %w", asset, err)
		}
		assets = []common.Asset{assetObj}
	} else {
		iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
		defer iterator.Close()
		for ; iterator.Valid(); iterator.Next() {
			var pool Pool
			if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
				return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
			}

			// Display the swap slips of Available-pool Layer 1 assets.
			if pool.Status != PoolAvailable || pool.Asset.IsNative() {
				continue
			}
			assets = append(assets, pool.Asset)
		}
	}

	result := make([]*types.QueryPoolSlipResponse, len(assets))
	for i := range assets {
		result[i] = &types.QueryPoolSlipResponse{}
		result[i].Asset = assets[i].String()

		poolSlip, err := qs.mgr.Keeper().GetPoolSwapSlip(ctx, ctx.BlockHeight(), assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get swap slip for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].PoolSlip = poolSlip.Int64()

		rollupCount, err := qs.mgr.Keeper().GetRollupCount(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get rollup count for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].RollupCount = rollupCount

		longRollup, err := qs.mgr.Keeper().GetLongRollup(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get long rollup for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].LongRollup = longRollup

		rollup, err := qs.mgr.Keeper().GetCurrentRollup(ctx, assets[i])
		if err != nil {
			return nil, fmt.Errorf("fail to get rollup count for asset (%s) height (%d), err:%w", assets[i], ctx.BlockHeight(), err)
		}
		result[i].Rollup = rollup
	}

	// For performance, only sum the rollup swap slip for comparison
	// when a single asset has been specified.
	if len(assets) == 1 {
		maxAnchorBlocks := qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxAnchorBlocks)
		var summedRollup int64
		for i := ctx.BlockHeight() - maxAnchorBlocks; i < ctx.BlockHeight(); i++ {
			poolSlip, err := qs.mgr.Keeper().GetPoolSwapSlip(ctx, i, assets[0])
			if err != nil {
				// Log the error, zero the sum, and exit the loop.
				ctx.Logger().Error("fail to get swap slip", "error", err, "asset", assets[0], "height", i)
				summedRollup = 0
				break
			}
			summedRollup += poolSlip.Int64()
		}
		result[0].SummedRollup = summedRollup
	}

	return &types.QueryPoolSlipsResponse{PoolSlips: result}, nil
}

func (qs queryServer) queryDerivedPool(ctx cosmos.Context, req *types.QueryDerivedPoolRequest) (*types.QueryDerivedPoolResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	if !asset.IsDerivedAsset() {
		return nil, fmt.Errorf("asset is not a derived asset: %s", asset)
	}

	// call begin block so the derived depth matches the next block execution state
	_ = qs.mgr.NetworkMgr().BeginBlock(ctx.WithBlockHeight(ctx.BlockHeight()+1), qs.mgr)

	// sum rune depth of anchor pools
	runeDepth := sdkmath.ZeroUint()
	for _, anchor := range qs.mgr.Keeper().GetAnchors(ctx, asset) {
		aPool, _ := qs.mgr.Keeper().GetPool(ctx, anchor)
		runeDepth = runeDepth.Add(aPool.BalanceRune)
	}

	dpool, _ := qs.mgr.Keeper().GetPool(ctx, asset.GetDerivedAsset())
	dbps := cosmos.ZeroUint()
	if dpool.Status == PoolAvailable {
		dbps = common.GetUncappedShare(dpool.BalanceRune, runeDepth, cosmos.NewUint(constants.MaxBasisPts))
	}

	p := types.QueryDerivedPoolResponse{
		Asset:        dpool.Asset.String(),
		Status:       dpool.Status.String(),
		Decimals:     dpool.Decimals,
		BalanceAsset: dpool.BalanceAsset.String(),
		BalanceRune:  dpool.BalanceRune.String(),
	}
	p.DerivedDepthBps = dbps.String()

	return &p, nil
}

func (qs queryServer) queryDerivedPools(ctx cosmos.Context, _ *types.QueryDerivedPoolsRequest) (*types.QueryDerivedPoolsResponse, error) {
	pools := make([]*types.QueryDerivedPoolResponse, 0)
	iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
		}
		// Ignore derived assets (except TOR)
		if !pool.Asset.IsDerivedAsset() {
			continue
		}

		runeDepth, _, _ := qs.mgr.NetworkMgr().CalcAnchor(ctx, qs.mgr, pool.Asset)
		dpool, _ := qs.mgr.Keeper().GetPool(ctx, pool.Asset.GetDerivedAsset())
		dbps := cosmos.ZeroUint()
		if dpool.Status == PoolAvailable {
			dbps = common.GetUncappedShare(
				dpool.BalanceRune,
				runeDepth,
				cosmos.NewUint(constants.MaxBasisPts),
			)
		}

		p := types.QueryDerivedPoolResponse{
			Asset:        dpool.Asset.String(),
			Status:       dpool.Status.String(),
			Decimals:     dpool.Decimals,
			BalanceAsset: dpool.BalanceAsset.String(),
			BalanceRune:  dpool.BalanceRune.String(),
		}
		p.DerivedDepthBps = dbps.String()

		pools = append(pools, &p)
	}

	return &types.QueryDerivedPoolsResponse{Pools: pools}, nil
}

func (qs queryServer) queryTradeUnit(ctx cosmos.Context, req *types.QueryTradeUnitRequest) (*types.QueryTradeUnitResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	tu, err := qs.mgr.Keeper().GetTradeUnit(ctx, asset)
	if err != nil {
		ctx.Logger().Error("fail to get trade unit", "error", err)
		return nil, fmt.Errorf("could not get trade unit: %w", err)
	}
	tuResp := types.QueryTradeUnitResponse{
		Asset: tu.Asset.String(),
		Units: tu.Units.String(),
		Depth: tu.Depth.String(),
	}
	return &tuResp, nil
}

func (qs queryServer) queryTradeUnits(ctx cosmos.Context, _ *types.QueryTradeUnitsRequest) (*types.QueryTradeUnitsResponse, error) {
	pools, err := qs.mgr.Keeper().GetPools(ctx)
	if err != nil {
		return nil, errors.New("failed to get pools")
	}
	units := make([]*types.QueryTradeUnitResponse, 0)
	for _, pool := range pools {
		// skip non-layer1 pools
		if pool.Asset.GetChain().IsTHORChain() {
			continue
		}
		asset := pool.Asset.GetTradeAsset()
		tu, err := qs.mgr.Keeper().GetTradeUnit(ctx, asset)
		if err != nil {
			ctx.Logger().Error("fail to get trade unit", "error", err)
			return nil, fmt.Errorf("could not get trade unit: %w", err)
		}
		tuResp := types.QueryTradeUnitResponse{
			Asset: tu.Asset.String(),
			Units: tu.Units.String(),
			Depth: tu.Depth.String(),
		}
		units = append(units, &tuResp)
	}

	return &types.QueryTradeUnitsResponse{TradeUnits: units}, nil
}

func (qs queryServer) queryTradeAccounts(ctx cosmos.Context, req *types.QueryTradeAccountsRequest) (*types.QueryTradeAccountsResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse address", "error", err)
		return nil, fmt.Errorf("could not parse address: %w", err)
	}

	accounts := make([]*types.QueryTradeAccountResponse, 0)
	iter := qs.mgr.Keeper().GetTradeAccountIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var ta TradeAccount
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &ta); err != nil {
			continue
		}
		if !ta.Asset.Equals(asset) {
			continue
		}
		if ta.Units.IsZero() {
			continue
		}
		taResp := types.QueryTradeAccountResponse{
			Asset:              ta.Asset.String(),
			Units:              ta.Units.String(),
			Owner:              ta.Owner.String(),
			LastAddHeight:      ta.LastAddHeight,
			LastWithdrawHeight: ta.LastWithdrawHeight,
		}
		accounts = append(accounts, &taResp)
	}

	return &types.QueryTradeAccountsResponse{TradeAccounts: accounts}, nil
}

func (qs queryServer) queryTradeAccount(ctx cosmos.Context, req *types.QueryTradeAccountRequest) (*types.QueryTradeAccountsResponse, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse address", "error", err)
		return nil, fmt.Errorf("could not parse address: %w", err)
	}

	accounts := make([]*types.QueryTradeAccountResponse, 0)
	iter := qs.mgr.Keeper().GetTradeAccountIteratorWithAddress(ctx, addr)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var ta TradeAccount
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &ta); err != nil {
			continue
		}
		if ta.Units.IsZero() {
			continue
		}

		taResp := types.QueryTradeAccountResponse{
			Asset:              ta.Asset.String(),
			Units:              ta.Units.String(),
			Owner:              ta.Owner.String(),
			LastAddHeight:      ta.LastAddHeight,
			LastWithdrawHeight: ta.LastWithdrawHeight,
		}
		accounts = append(accounts, &taResp)
	}

	return &types.QueryTradeAccountsResponse{TradeAccounts: accounts}, nil
}

func (qs queryServer) querySecuredAssets(ctx cosmos.Context, req *types.QuerySecuredAssetsRequest) (*types.QuerySecuredAssetsResponse, error) {
	iter := qs.mgr.Keeper().GetSecuredAssetIterator(ctx)
	res := make([]*types.QuerySecuredAssetResponse, 0)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var p keeper.SecuredAsset
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &p); err != nil {
			continue
		}
		if p.Depth.IsZero() {
			continue
		}

		shareSupply := qs.mgr.SecuredAssetManager().GetShareSupply(ctx, p.Asset)

		pResp := types.QuerySecuredAssetResponse{
			Asset:  p.Asset.String(),
			Supply: shareSupply.String(),
			Depth:  p.Depth.String(),
		}
		res = append(res, &pResp)
	}

	return &types.QuerySecuredAssetsResponse{
		Assets: res,
	}, nil
}

func (qs queryServer) querySecuredAsset(ctx cosmos.Context, req *types.QuerySecuredAssetRequest) (*types.QuerySecuredAssetResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}

	a, shareSupply, err := qs.mgr.SecuredAssetManager().GetSecuredAssetStatus(ctx, asset)
	if err != nil {
		ctx.Logger().Error("fail to get asset status", "error", err)
		return nil, fmt.Errorf("could not get asset status: %w", err)
	}

	return &types.QuerySecuredAssetResponse{
		Asset:  a.Asset.String(),
		Supply: shareSupply.String(),
		Depth:  a.Depth.String(),
	}, nil
}

func (qs queryServer) queryStableReserve(ctx cosmos.Context, _ *types.QueryStableReserveRequest) (*types.QueryStableReserveResponse, error) {
	moduleAddr := qs.mgr.Keeper().GetModuleAccAddress(StableReserveName)
	securedMgr := qs.mgr.SecuredAssetManager()
	assets := make(map[string]common.Asset)

	for _, anchor := range qs.mgr.Keeper().GetAnchors(ctx, common.TOR) {
		asset := anchor.GetLayer1Asset()
		assets[asset.String()] = asset
	}

	iter := qs.mgr.Keeper().GetSecuredAssetIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var secured keeper.SecuredAsset
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &secured); err != nil {
			continue
		}

		asset := secured.Asset.GetLayer1Asset()
		if securedMgr.BalanceOf(ctx, asset, moduleAddr).IsZero() {
			continue
		}
		assets[asset.String()] = asset
	}

	keys := slices.Sorted(maps.Keys(assets))

	inventory := make([]*types.QueryStableReserveInventory, 0, len(keys))
	for _, key := range keys {
		asset := assets[key]
		healthy, reason := qs.mgr.StableReserveManager().IsEligibleStable(ctx, asset)
		oraclePrice := ""
		if price, err := qs.mgr.Keeper().GetPrice(ctx, asset.Ticker.String()); err == nil {
			oraclePrice = price.Price
		}

		inventory = append(inventory, &types.QueryStableReserveInventory{
			Asset:       asset.String(),
			Amount:      securedMgr.BalanceOf(ctx, asset, moduleAddr).String(),
			Healthy:     healthy,
			Reason:      reason,
			OraclePrice: oraclePrice,
		})
	}

	return &types.QueryStableReserveResponse{
		Enabled:           qs.mgr.StableReserveManager().IsEnabled(ctx),
		RuneBalance:       qs.mgr.Keeper().GetRuneBalanceOfModule(ctx, StableReserveName).String(),
		DepegToleranceBps: strconv.FormatInt(qs.mgr.Keeper().GetConfigInt64(ctx, constants.StableReserveDepegToleranceBps), 10),
		PolShareBps:       strconv.FormatInt(qs.mgr.Keeper().GetConfigInt64(ctx, constants.StableReservePOLShareBps), 10),
		Inventory:         inventory,
		// Deployment swaps always use quantity 0 / interval 0 (see
		// scheduleDeploymentSwap); these are no longer configurable.
		DeploymentStreamQuantity: "0",
		DeploymentStreamInterval: "0",
		ActiveSwaps:              qs.queryStableReserveActiveSwaps(ctx),
	}, nil
}

func (qs queryServer) queryStableReserveActiveSwaps(ctx cosmos.Context) []*types.QueryStableReserveStreamingSwap {
	stableReserveAddress, err := qs.mgr.Keeper().GetModuleAddress(StableReserveName)
	if err != nil {
		return nil
	}

	active := make([]*types.QueryStableReserveStreamingSwap, 0)
	appendActive := func(iter cosmos.Iterator) {
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var msg MsgSwap
			if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &msg); err != nil {
				continue
			}
			purpose, ok := stableReserveSwapPurpose(msg.Tx.Memo)
			if !ok {
				continue
			}
			if !msg.Tx.FromAddress.Equals(stableReserveAddress) {
				continue
			}

			sourceAsset := ""
			amount := "0"
			if len(msg.Tx.Coins) > 0 {
				sourceAsset = msg.Tx.Coins[0].Asset.String()
				amount = msg.Tx.Coins[0].Amount.String()
			}
			active = append(active, &types.QueryStableReserveStreamingSwap{
				TxId:           msg.Tx.ID.String(),
				Memo:           msg.Tx.Memo,
				Purpose:        purpose,
				SourceAsset:    sourceAsset,
				TargetAsset:    msg.TargetAsset.String(),
				Amount:         amount,
				StreamQuantity: msg.StreamQuantity,
				StreamInterval: msg.StreamInterval,
			})
		}
	}
	appendActive(qs.mgr.Keeper().GetSwapQueueIterator(ctx))
	appendActive(qs.mgr.Keeper().GetAdvSwapQueueItemIterator(ctx))

	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Purpose != active[j].Purpose {
			return active[i].Purpose < active[j].Purpose
		}
		return active[i].Memo < active[j].Memo
	})
	return active
}

func stableReserveSwapPurpose(memo string) (string, bool) {
	key, ok := types.StableReservePendingSwapKeyFromMemo(memo)
	if !ok {
		return "", false
	}
	switch {
	case key == types.StableReservePendingDeploySwapKey:
		return "deployment", true
	case strings.HasPrefix(key, types.StableReservePendingRescueSwapPrefix):
		return "rescue", true
	default:
		return "", false
	}
}

func (qs queryServer) queryStableReserveContributor(ctx cosmos.Context, req *types.QueryStableReserveContributorRequest) (*types.QueryStableReserveContributorResponse, error) {
	if len(req.Asset) == 0 {
		return nil, errors.New("asset not provided")
	}
	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		ctx.Logger().Error("fail to parse asset", "error", err)
		return nil, fmt.Errorf("could not parse asset: %w", err)
	}
	asset = asset.GetLayer1Asset()
	if !asset.IsExternalL1Asset() {
		return nil, fmt.Errorf("invalid stable reserve asset: %s", req.Asset)
	}
	if len(req.Address) == 0 {
		return nil, errors.New("address not provided")
	}
	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse address", "error", err)
		return nil, fmt.Errorf("could not parse address: %w", err)
	}

	credit, err := qs.mgr.Keeper().GetStableReserveCredit(ctx, asset, addr)
	if err != nil {
		return nil, fmt.Errorf("could not get stable reserve contributor credit: %w", err)
	}

	return &types.QueryStableReserveContributorResponse{
		Asset:   asset.String(),
		Address: addr.String(),
		Credit:  credit.String(),
	}, nil
}

func extractVoter(ctx cosmos.Context, tx_id string, mgr *Mgrs, outbound bool) (common.TxID, ObservedTxVoter, error) {
	if len(tx_id) == 0 {
		return "", ObservedTxVoter{}, errors.New("tx id not provided")
	}
	hash, err := common.NewTxID(tx_id)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return "", ObservedTxVoter{}, fmt.Errorf("fail to parse tx id: %w", err)
	}
	var voter ObservedTxVoter
	if outbound {
		voter, err = mgr.Keeper().GetObservedTxOutVoter(ctx, hash)
	} else {
		voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, hash)
	}
	if err != nil {
		ctx.Logger().Error("fail to get observed tx voter", "error", err)
		return "", ObservedTxVoter{}, fmt.Errorf("fail to get observed tx voter: %w", err)
	}
	return hash, voter, nil
}

func (qs queryServer) queryTxVoters(ctx cosmos.Context, req *types.QueryTxVotersRequest) (*types.QueryObservedTxVoter, error) {
	hash, voter, err := extractVoter(ctx, req.TxId, qs.mgr, req.Outbound)
	if err != nil {
		return nil, err
	}
	// Migrations can have both voters under one tx ID. Explicit outbound
	// queries must never fall back to destination-side observation metadata.
	if !req.Outbound && len(voter.Txs) == 0 {
		voter, err = qs.mgr.Keeper().GetObservedTxOutVoter(ctx, hash)
		if err != nil {
			return nil, fmt.Errorf("fail to get observed tx out voter: %w", err)
		}
	}
	if len(voter.Txs) == 0 {
		return nil, fmt.Errorf("tx: %s doesn't exist", hash)
	}

	var txs []types.QueryObservedTx
	// Leave this nil (null rather than []) if the source is nil.
	if voter.Txs != nil {
		txs = make([]types.QueryObservedTx, len(voter.Txs))
		for i := range voter.Txs {
			txs[i] = castObservedTx(voter.Txs[i])
		}
	}

	return &types.QueryObservedTxVoter{
		TxID:            voter.TxID,
		Tx:              castObservedTx(voter.Tx),
		Height:          voter.Height,
		Txs:             txs,
		Actions:         voter.Actions,
		OutTxs:          voter.OutTxs,
		FinalisedHeight: voter.FinalisedHeight,
		UpdatedVault:    voter.UpdatedVault,
		Reverted:        voter.Reverted,
		OutboundHeight:  voter.OutboundHeight,
	}, nil
}

// TODO: Remove isSwap and isPending code when SwapFinalised field deprecated.
func checkPending(ctx cosmos.Context, keeper keeper.Keeper, voter ObservedTxVoter) (isSwap, isPending, pending bool, streamingSwap StreamingSwap) {
	// If there's no (confirmation-counting-complete) consensus transaction yet, don't spend time checking the swap status.
	if voter.Tx.IsEmpty() || !voter.Tx.IsFinal() {
		return
	}

	pending = keeper.HasSwapQueueItem(ctx, voter.TxID, 0) || keeper.HasAdvSwapQueueItem(ctx, voter.TxID, 0)

	// Only look for streaming information when a swap is pending.
	if pending {
		var err error
		streamingSwap, err = keeper.GetStreamingSwap(ctx, voter.TxID)
		if err != nil {
			// Log the error, but continue without streaming information.
			ctx.Logger().Error("fail to get streaming swap", "error", err)
		}
	}

	memo, err := ParseMemoWithTHORNames(ctx, keeper, voter.Tx.Tx.Memo)
	if err != nil {
		// If unable to parse, assume not a (valid) swap or limit swap memo.
		return
	}

	memoType := memo.GetType()
	// If the memo asset is a synth, as with Savers add liquidity or withdraw, a swap is assumed to be involved.
	if memoType == TxSwap || memoType == TxLimitSwap || memo.GetAsset().IsSyntheticAsset() {
		isSwap = true
		// Only check the KVStore when the inbound transaction has already been finalised
		// and when there haven't been any Actions planned.
		// This will also check the KVStore when an inbound transaction has no output,
		// such as the output being not enough to cover a fee.
		if voter.FinalisedHeight != 0 && len(voter.Actions) == 0 {
			// Use of Swap Queue or Adv Swap Queue depends on Mimir key AdvSwapQueueBooks rather than memo type, so check both.
			isPending = pending
		}
	}

	return
}

// Get the largest number of signers for a not-final (pre-confirmation-counting) and final Txs respectively.
func countSigners(voter ObservedTxVoter) (int64, int64) {
	var notFinalCount, finalCount int
	for i, refTx := range voter.Txs {
		signersMap := make(map[string]bool)
		final := refTx.IsFinal()
		for f, tx := range voter.Txs {
			// Earlier Txs already checked against all, so no need to check,
			// but do include the signers of the current Txs.
			if f < i {
				continue
			}
			// Count larger number of signers for not-final and final observations separately.
			if tx.IsFinal() != final {
				continue
			}
			if !refTx.Tx.EqualsEx(tx.Tx) {
				continue
			}

			for _, signer := range tx.GetSigners() {
				signersMap[signer.String()] = true
			}
		}
		if final && len(signersMap) > finalCount {
			finalCount = len(signersMap)
		} else if !final && len(signersMap) > notFinalCount {
			notFinalCount = len(signersMap)
		}
	}
	return int64(notFinalCount), int64(finalCount)
}

// Call newTxStagesResponse from both queryTxStatus (which includes the stages) and queryTxStages.
// TODO: Remove isSwap and isPending arguments when SwapFinalised deprecated in favour of SwapStatus.
// TODO: Deprecate InboundObserved.Started field in favour of the observation counting.
func newTxStagesResponse(ctx cosmos.Context, voter ObservedTxVoter, isSwap, isPending, pending bool, streamingSwap StreamingSwap) (result types.QueryTxStagesResponse) {
	result.InboundObserved.PreConfirmationCount, result.InboundObserved.FinalCount = countSigners(voter)
	result.InboundObserved.Completed = !voter.Tx.IsEmpty()

	// If not Completed, fill in Started and do not proceed.
	if !result.InboundObserved.Completed {
		obStart := (len(voter.Txs) != 0)
		result.InboundObserved.Started = obStart
		return result
	}

	// Current block height is relevant in the confirmation counting and outbound stages.
	currentHeight := ctx.BlockHeight()

	// Only fill in InboundConfirmationCounted when confirmation counting took place.
	if voter.Height != 0 {
		var confCount types.InboundConfirmationCountedStage

		// Set the Completed state first.
		extObsHeight := voter.Tx.BlockHeight
		extConfDelayHeight := voter.Tx.FinaliseHeight
		confCount.Completed = !(extConfDelayHeight > extObsHeight)

		// Only fill in other fields if not Completed.
		if !confCount.Completed {
			countStartHeight := voter.Height
			confCount.CountingStartHeight = countStartHeight
			confCount.Chain = voter.Tx.Tx.Chain.String()
			confCount.ExternalObservedHeight = extObsHeight
			confCount.ExternalConfirmationDelayHeight = extConfDelayHeight

			estConfMs := voter.Tx.Tx.Chain.ApproximateBlockMilliseconds() * (extConfDelayHeight - extObsHeight)
			if currentHeight > countStartHeight {
				estConfMs -= (currentHeight - countStartHeight) * common.THORChain.ApproximateBlockMilliseconds()
			}
			estConfSec := estConfMs / 1000
			// Floor at 0.
			if estConfSec < 0 {
				estConfSec = 0
			}
			confCount.RemainingConfirmationSeconds = estConfSec
		}

		result.InboundConfirmationCounted = &confCount
	}

	var inboundFinalised types.InboundFinalisedStage
	inboundFinalised.Completed = (voter.FinalisedHeight != 0)
	result.InboundFinalised = &inboundFinalised

	var swapStatus types.SwapStatus
	swapStatus.Pending = pending
	// Only display the SwapStatus stage's Streaming field when there's streaming information available.
	if streamingSwap.Valid() == nil {
		streaming := types.StreamingStatus{
			Interval: int64(streamingSwap.Interval),
			Quantity: int64(streamingSwap.Quantity),
			Count:    int64(streamingSwap.Count),
		}
		swapStatus.Streaming = &streaming
	}
	result.SwapStatus = &swapStatus

	// Whether there's an external outbound or not, show the SwapFinalised stage from the start.
	if isSwap {
		var swapFinalisedState types.SwapFinalisedStage

		swapFinalisedState.Completed = false
		if !isPending && result.InboundFinalised.Completed {
			// Record as completed only when not pending after the inbound has already been finalised.
			swapFinalisedState.Completed = true
		}

		result.SwapFinalised = &swapFinalisedState
	}

	// Only fill ExternalOutboundDelay and ExternalOutboundKeysign for inbound transactions with an external outbound;
	// namely, transactions with an outbound_height .
	if voter.OutboundHeight == 0 {
		return result
	}

	// Only display the OutboundDelay stage when there's a delay.
	if voter.OutboundHeight > voter.FinalisedHeight {
		var outDelay types.OutboundDelayStage

		// Set the Completed state first.
		outDelay.Completed = (currentHeight >= voter.OutboundHeight)

		// Only fill in other fields if not Completed.
		if !outDelay.Completed {
			remainBlocks := voter.OutboundHeight - currentHeight
			outDelay.RemainingDelayBlocks = remainBlocks

			remainSec := remainBlocks * common.THORChain.ApproximateBlockMilliseconds() / 1000
			outDelay.RemainingDelaySeconds = remainSec
		}

		result.OutboundDelay = &outDelay
	}

	var outSigned types.OutboundSignedStage

	// Set the Completed state first.
	outSigned.Completed = (voter.Tx.Status != common.Status_incomplete)

	// Only fill in other fields if not Completed.
	if !outSigned.Completed {
		scheduledHeight := voter.OutboundHeight
		outSigned.ScheduledOutboundHeight = scheduledHeight

		// Only fill in BlocksSinceScheduled if the outbound delay is complete.
		if currentHeight >= scheduledHeight {
			sinceScheduled := currentHeight - scheduledHeight
			outSigned.BlocksSinceScheduled = &types.ProtoInt64{Value: sinceScheduled}
		}
	}

	result.OutboundSigned = &outSigned

	return result
}

func (qs queryServer) queryTxStages(ctx cosmos.Context, req *types.QueryTxStagesRequest) (*types.QueryTxStagesResponse, error) {
	// First, get the ObservedTxVoter of interest.
	_, voter, err := extractVoter(ctx, req.TxId, qs.mgr, false)
	if err != nil {
		return nil, err
	}
	// when no TxIn voter don't check TxOut voter, as TxOut THORChain observation or not matters little to the user once signed and broadcast
	// Rather than a "tx: %s doesn't exist" result, allow a response to an existing-but-unobserved hash with Observation.Started 'false'.

	isSwap, isPending, pending, streamingSwap := checkPending(ctx, qs.mgr.Keeper(), voter)

	result := newTxStagesResponse(ctx, voter, isSwap, isPending, pending, streamingSwap)

	return &result, nil
}

func (qs queryServer) queryTxStatus(ctx cosmos.Context, req *types.QueryTxStatusRequest) (*types.QueryTxStatusResponse, error) {
	// First, get the ObservedTxVoter of interest.
	_, voter, err := extractVoter(ctx, req.TxId, qs.mgr, false)
	if err != nil {
		return nil, err
	}
	// when no TxIn voter don't check TxOut voter, as TxOut THORChain observation or not matters little to the user once signed and broadcast
	// Rather than a "tx: %s doesn't exist" result, allow a response to an existing-but-unobserved hash with Stages.Observation.Started 'false'.

	// TODO: Remove isSwap and isPending arguments when SwapFinalised deprecated.
	isSwap, isPending, pending, streamingSwap := checkPending(ctx, qs.mgr.Keeper(), voter)

	var result types.QueryTxStatusResponse

	// If there's a consensus Tx, display that.
	// If not, but there's at least one observation, display the first observation's Tx.
	// If there are no observations yet, don't display a Tx (only showing the 'Observation' stage with 'Started' false).
	if !voter.Tx.Tx.IsEmpty() {
		result.Tx = &voter.Tx.Tx
	} else if len(voter.Txs) > 0 {
		result.Tx = &voter.Txs[0].Tx
	}

	// Leave this nil (null rather than []) if the source is nil.
	if voter.Actions != nil {
		result.PlannedOutTxs = make([]*types.PlannedOutTx, len(voter.Actions))
		for i := range voter.Actions {
			result.PlannedOutTxs[i] = &types.PlannedOutTx{
				Chain:     voter.Actions[i].Chain.String(),
				ToAddress: voter.Actions[i].ToAddress.String(),
				Coin:      &voter.Actions[i].Coin,
				Refund:    strings.HasPrefix(voter.Actions[i].GetMemo(), "REFUND"),
			}
		}
	}

	// Leave this nil (null rather than []) if the source is nil.
	if voter.OutTxs != nil {
		result.OutTxs = voter.OutTxs
	}

	result.Stages = newTxStagesResponse(ctx, voter, isSwap, isPending, pending, streamingSwap)

	return &result, nil
}

func (qs queryServer) queryTx(ctx cosmos.Context, req *types.QueryTxRequest) (*types.QueryTxResponse, error) {
	hash, voter, err := extractVoter(ctx, req.TxId, qs.mgr, false)
	if err != nil {
		return nil, err
	}
	if len(voter.Txs) == 0 {
		voter, err = qs.mgr.Keeper().GetObservedTxOutVoter(ctx, hash)
		if err != nil {
			return nil, fmt.Errorf("fail to get observed tx out voter: %w", err)
		}
		if len(voter.Txs) == 0 {
			return nil, fmt.Errorf("tx: %s doesn't exist", hash)
		}
	}

	nodeAccounts, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get node accounts: %w", err)
	}
	keysignMetric, err := qs.mgr.Keeper().GetTssKeysignMetric(ctx, hash)
	if err != nil {
		ctx.Logger().Error("fail to get keysign metrics", "error", err)
	}

	result := types.QueryTxResponse{
		ObservedTx:      castObservedTx(*voter.GetTx(nodeAccounts)),
		ConsensusHeight: voter.Height,
		FinalisedHeight: voter.FinalisedHeight,
		OutboundHeight:  voter.OutboundHeight,
		KeysignMetric:   keysignMetric,
	}

	return &result, nil
}

func extractBlockHeight(ctx cosmos.Context, heightStr string) (int64, error) {
	return parseQueryHeight(ctx, heightStr, false)
}

// parseQueryHeight parses historical query heights for endpoint handlers and
// rejects future heights so callers cannot evaluate unavailable state.
func parseQueryHeight(ctx cosmos.Context, heightStr string, allowEmpty bool) (int64, error) {
	if len(heightStr) == 0 {
		if allowEmpty {
			return ctx.BlockHeight(), nil
		}
		return -1, errors.New("block height not provided")
	}
	height, err := strconv.ParseInt(heightStr, 10, 64)
	if err != nil {
		ctx.Logger().Error("fail to parse block height", "error", err)
		return -1, fmt.Errorf("fail to parse block height: %w", err)
	}
	if height < 0 {
		return -1, fmt.Errorf("block height must not be negative")
	}
	if height > ctx.BlockHeight() {
		return -1, fmt.Errorf("block height not available yet")
	}
	return height, nil
}

func (qs queryServer) queryKeygen(ctx cosmos.Context, req *types.QueryKeygenRequest) (*types.QueryKeygenResponse, error) {
	height, err := extractBlockHeight(ctx, req.Height)
	if err != nil {
		return nil, err
	}

	keygenBlock, err := qs.mgr.Keeper().GetKeygenBlock(ctx, height)
	if err != nil {
		ctx.Logger().Error("fail to get keygen block", "error", err)
		return nil, fmt.Errorf("fail to get keygen block: %w", err)
	}

	if len(req.PubKey) > 0 {
		var pk common.PubKey
		pk, err = common.NewPubKey(req.PubKey)
		if err != nil {
			ctx.Logger().Error("fail to parse pubkey", "error", err)
			return nil, fmt.Errorf("fail to parse pubkey: %w", err)
		}
		// only return those keygen contains the request pub key
		newKeygenBlock := NewKeygenBlock(keygenBlock.Height)
		for _, keygen := range keygenBlock.Keygens {
			if keygen.GetMembers().Contains(pk) {
				newKeygenBlock.Keygens = append(newKeygenBlock.Keygens, keygen)
			}
		}
		keygenBlock = newKeygenBlock
	}

	buf, err := json.Marshal(keygenBlock)
	if err != nil {
		ctx.Logger().Error("fail to marshal keygen block to json", "error", err)
		return nil, fmt.Errorf("fail to marshal keygen block to json: %w", err)
	}
	// TODO: confirm this signing mode which is only for ledger devices.
	// Not applicable if ledger devices will never be used.
	// SIGN_MODE_LEGACY_AMINO_JSON will be removed in the future for SIGN_MODE_TEXTUAL
	signingMode := signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON
	sig, _, err := qs.kbs.Keybase.Sign("thorchain", buf, signingMode)
	if err != nil {
		ctx.Logger().Error("fail to sign keygen", "error", err)
		return nil, fmt.Errorf("fail to sign keygen: %w", err)
	}

	return &types.QueryKeygenResponse{
		KeygenBlock: &keygenBlock,
		Signature:   base64.StdEncoding.EncodeToString(sig),
	}, nil
}

func (qs queryServer) queryKeysign(ctx cosmos.Context, heightStr, pubKey string) (*types.QueryKeysignResponse, error) {
	height, err := extractBlockHeight(ctx, heightStr)
	if err != nil {
		return nil, err
	}

	pk := common.EmptyPubKey
	if len(pubKey) > 0 {
		pk, err = common.NewPubKey(pubKey)
		if err != nil {
			ctx.Logger().Error("fail to parse pubkey", "error", err)
			return nil, fmt.Errorf("fail to parse pubkey: %w", err)
		}
	}

	txs, err := qs.mgr.Keeper().GetTxOut(ctx, height)
	if err != nil {
		ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
		return nil, fmt.Errorf("fail to get tx out array from key value store: %w", err)
	}

	if !pk.IsEmpty() {
		newTxs := &TxOut{
			Height: txs.Height,
		}
		for _, tx := range txs.TxArray {
			if pk.Equals(tx.VaultPubKey) {
				zero := cosmos.ZeroUint()
				if tx.CloutSpent == nil {
					tx.CloutSpent = &zero
				}
				newTxs.TxArray = append(newTxs.TxArray, tx)
			}
		}
		txs = newTxs
	}

	buf, err := json.Marshal(txs)
	if err != nil {
		ctx.Logger().Error("fail to marshal keysign block to json", "error", err)
		return nil, fmt.Errorf("fail to marshal keysign block to json: %w", err)
	}
	// TODO: confirm this signing mode which is only for ledger devices.
	// Not applicable if ledger devices will never be used.
	// SIGN_MODE_LEGACY_AMINO_JSON will be removed in the future for SIGN_MODE_TEXTUAL
	signingMode := signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON
	sig, _, err := qs.kbs.Keybase.Sign("thorchain", buf, signingMode)
	if err != nil {
		ctx.Logger().Error("fail to sign keysign", "error", err)
		return nil, fmt.Errorf("fail to sign keysign: %w", err)
	}

	return &types.QueryKeysignResponse{
		Keysign:   txs,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// queryOutQueue - iterates over txout, counting how many transactions are waiting to be sent
func (qs queryServer) queryQueue(ctx cosmos.Context, _ *types.QueryQueueRequest) (*types.QueryQueueResponse, error) {
	constAccessor := qs.mgr.GetConstants()
	signingTransactionPeriod := qs.mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	// XMR and EVM token/aggregator outbounds stay live for an extended signing
	// period; scan that window and apply each item's own period below.
	startHeight := ctx.BlockHeight() - types.MaxTxOutSigningTransactionPeriod(signingTransactionPeriod)
	var query types.QueryQueueResponse
	scheduledOutboundValue := cosmos.ZeroUint()
	scheduledOutboundClout := cosmos.ZeroUint()

	iterator := qs.mgr.Keeper().GetSwapQueueIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var msg MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &msg); err != nil {
			continue
		}
		query.Swap++
	}

	iter2 := qs.mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
	defer iter2.Close()
	for ; iter2.Valid(); iter2.Next() {
		var msg MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter2.Value(), &msg); err != nil {
			ctx.Logger().Error("failed to load MsgSwap", "error", err)
			continue
		}
		query.Swap++
	}

	for height := startHeight; height <= ctx.BlockHeight(); height++ {
		txs, err := qs.mgr.Keeper().GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			return nil, fmt.Errorf("fail to get tx out array from key value store: %w", err)
		}
		for _, tx := range txs.TxArray {
			if tx.OutHash.IsEmpty() && height >= ctx.BlockHeight()-tx.SigningTransactionPeriod(signingTransactionPeriod) {
				memo, _ := ParseMemoWithTHORNames(ctx, qs.mgr.Keeper(), tx.GetMemo())
				if memo.IsInternal() {
					query.Internal++
				} else if memo.IsOutbound() {
					query.Outbound++
				}
			}
		}
	}

	// sum outbound value
	maxTxOutOffset, err := qs.mgr.Keeper().GetMimir(ctx, constants.MaxTxOutOffset.String())
	if maxTxOutOffset < 0 || err != nil {
		maxTxOutOffset = constAccessor.GetInt64Value(constants.MaxTxOutOffset)
	}
	txOutDelayMax, err := qs.mgr.Keeper().GetMimir(ctx, constants.TxOutDelayMax.String())
	if txOutDelayMax <= 0 || err != nil {
		txOutDelayMax = constAccessor.GetInt64Value(constants.TxOutDelayMax)
	}

	for height := ctx.BlockHeight() + 1; height <= ctx.BlockHeight()+txOutDelayMax; height++ {
		value, clout, err := qs.mgr.Keeper().GetTxOutValue(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			continue
		}
		if height > ctx.BlockHeight()+maxTxOutOffset && value.IsZero() {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		scheduledOutboundValue = scheduledOutboundValue.Add(value)
		scheduledOutboundClout = scheduledOutboundClout.Add(clout)
	}

	query.ScheduledOutboundValue = scheduledOutboundValue.String()
	query.ScheduledOutboundClout = scheduledOutboundClout.String()

	return &query, nil
}

func (qs queryServer) queryLastBlockHeights(ctx cosmos.Context, chain string) (*types.QueryLastBlocksResponse, error) {
	var chains common.Chains
	if len(chain) > 0 {
		var err error
		chain, err := common.NewChain(chain)
		if err != nil {
			ctx.Logger().Error("fail to parse chain", "error", err, "chain", chain)
			return nil, fmt.Errorf("fail to retrieve chain: %w", err)
		}
		chains = append(chains, chain)
	} else {
		asgards, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return nil, fmt.Errorf("fail to get active asgard: %w", err)
		}
		for _, vault := range asgards {
			chains = vault.GetChains().Distinct()
			break
		}
	}
	var result []*types.ChainsLastBlock
	for _, c := range chains {
		if c == common.THORChain {
			continue
		}
		chainHeight, err := qs.mgr.Keeper().GetLastChainHeight(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("fail to get last chain height: %w", err)
		}

		signed, err := qs.mgr.Keeper().GetLastSignedHeight(ctx)
		if err != nil {
			return nil, fmt.Errorf("fail to get last sign height: %w", err)
		}
		result = append(result, &types.ChainsLastBlock{
			Chain:          c.String(),
			LastObservedIn: chainHeight,
			LastSignedOut:  signed,
			Thorchain:      ctx.BlockHeight(),
		})
	}

	return &types.QueryLastBlocksResponse{LastBlocks: result}, nil
}

func (qs queryServer) queryConstantValues(_ cosmos.Context, _ *types.QueryConstantValuesRequest) (*types.QueryConstantValuesResponse, error) {
	constAccessor := qs.mgr.GetConstants()
	cv := constAccessor.GetConstantValsByKeyname()

	proto := types.QueryConstantValuesResponse{}
	for _, k := range slices.Sorted(maps.Keys(cv.Int64Values)) {
		proto.Int_64Values = append(proto.Int_64Values, &types.Int64Constants{
			Name:  k,
			Value: cv.Int64Values[k],
		})
	}
	for _, k := range slices.Sorted(maps.Keys(cv.BoolValues)) {
		proto.BoolValues = append(proto.BoolValues, &types.BoolConstants{
			Name:  k,
			Value: cv.BoolValues[k],
		})
	}
	for _, k := range slices.Sorted(maps.Keys(cv.StringValues)) {
		proto.StringValues = append(proto.StringValues, &types.StringConstants{
			Name:  k,
			Value: cv.StringValues[k],
		})
	}

	return &proto, nil
}

func (qs queryServer) queryVersion(ctx cosmos.Context, _ *types.QueryVersionRequest) (*types.QueryVersionResponse, error) {
	v, hasV := qs.mgr.Keeper().GetVersionWithCtx(ctx)
	if !hasV {
		// re-compute version if not stored
		v = qs.mgr.Keeper().GetLowestActiveVersion(ctx)
	}

	minJoinLast, minJoinLastChangedHeight := qs.mgr.Keeper().GetMinJoinLast(ctx)

	ver := types.QueryVersionResponse{
		Current:         v.String(),
		Next:            minJoinLast.String(),
		NextSinceHeight: minJoinLastChangedHeight, // omitted if 0
		Querier:         constants.SWVersion.String(),
	}
	return &ver, nil
}

func (qs queryServer) queryUpgradeProposals(ctx cosmos.Context, req *types.QueryUpgradeProposalsRequest) (*types.QueryUpgradeProposalsResponse, error) {
	res := make([]*types.QueryUpgradeProposalResponse, 0)

	k := qs.mgr.Keeper()
	iter := k.GetUpgradeProposalIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, value := iter.Key(), iter.Value()

		nameSplit := strings.Split(string(key), "/")
		name := nameSplit[len(nameSplit)-1]

		var upgrade types.Upgrade
		if err := k.Cdc().Unmarshal(value, &upgrade); err != nil {
			return nil, fmt.Errorf("failed to unmarshal proposed upgrade: %w", err)
		}

		p, err := qs.queryUpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{Name: name})
		if err != nil {
			return nil, fmt.Errorf("failed to query upgrade proposal: %w", err)
		}

		res = append(res, p)
	}

	return &types.QueryUpgradeProposalsResponse{UpgradeProposals: res}, nil
}

func (qs queryServer) queryUpgradeProposal(ctx cosmos.Context, req *types.QueryUpgradeProposalRequest) (*types.QueryUpgradeProposalResponse, error) {
	if len(req.Name) == 0 {
		return nil, errors.New("upgrade name not provided")
	}

	k := qs.mgr.Keeper()

	proposal, err := k.GetProposedUpgrade(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("fail to get upgrade proposal: %w", err)
	}

	if proposal == nil {
		return nil, fmt.Errorf("upgrade proposal not found: %s", req.Name)
	}

	uq, err := keeperv1.UpgradeApprovedByMajority(ctx, k, req.Name)
	if err != nil {
		return nil, fmt.Errorf("fail to check upgrade approval: %w", err)
	}

	approval := big.NewRat(int64(uq.ApprovingVals), int64(uq.TotalActive))
	approvalFlt, _ := approval.Float64()
	approvalStr := fmt.Sprintf("%.2f", approvalFlt*100)

	vtq := int64(uq.NeededForQuorum)

	// gather the approvers and rejecters
	approvers := []string{}
	rejecters := []string{}
	iter := k.GetUpgradeVoteIterator(ctx, req.Name)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, value := iter.Key(), iter.Value()
		addr := cosmos.AccAddress(bytes.TrimPrefix(key, []byte(keeperv1.VotePrefix(req.Name))))
		if bytes.Equal(value, []byte{0x1}) {
			approvers = append(approvers, addr.String())
		} else {
			rejecters = append(rejecters, addr.String())
		}
	}

	res := types.QueryUpgradeProposalResponse{
		Name:               req.Name,
		Height:             proposal.Height,
		Info:               proposal.Info,
		Approved:           uq.Approved,
		ApprovedPercent:    approvalStr,
		ValidatorsToQuorum: vtq,
		Approvers:          approvers,
		Rejecters:          rejecters,
	}

	return &res, nil
}

func (qs queryServer) queryAccount(ctx cosmos.Context, req *types.QueryAccountRequest) (*types.QueryAccountResponse, error) {
	b32, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	acc := qs.mgr.Keeper().GetAccount(ctx, b32)

	var pubKey string
	pk := acc.GetPubKey()
	if pk != nil {
		pubKey = pk.String()
	}

	return &types.QueryAccountResponse{
		Address:       acc.GetAddress().String(),
		PubKey:        pubKey,
		AccountNumber: strconv.FormatUint(acc.GetAccountNumber(), 10),
		Sequence:      strconv.FormatUint(acc.GetSequence(), 10),
	}, nil
}

func (qs queryServer) queryBalances(ctx cosmos.Context, req *types.QueryBalancesRequest) (*types.QueryBalancesResponse, error) {
	b32, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	b := qs.mgr.Keeper().GetBalance(ctx, b32)

	balances := make([]*types.Amount, len(b))
	for i, bal := range b {
		balances[i] = &types.Amount{
			Denom:  bal.Denom,
			Amount: bal.Amount.String(),
		}
	}

	return &types.QueryBalancesResponse{
		Balances: balances,
	}, nil
}

func (qs queryServer) queryUpgradeVotes(ctx cosmos.Context, req *types.QueryUpgradeVotesRequest) (*types.QueryUpgradeVotesResponse, error) {
	if len(req.Name) == 0 {
		return nil, errors.New("upgrade name not provided")
	}

	prefix := []byte(keeperv1.VotePrefix(req.Name))
	res := make([]*types.UpgradeVote, 0)

	iter := qs.mgr.Keeper().GetUpgradeVoteIterator(ctx, req.Name)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, value := iter.Key(), iter.Value()

		addr := cosmos.AccAddress(bytes.TrimPrefix(key, prefix))

		var vote string
		if bytes.Equal(value, []byte{0x1}) {
			vote = "approve"
		} else {
			vote = "reject"
		}

		v := types.UpgradeVote{
			NodeAddress: addr.String(),
			Vote:        vote,
		}

		res = append(res, &v)
	}

	return &types.QueryUpgradeVotesResponse{UpgradeVotes: res}, nil
}

func (qs queryServer) queryMimirWithKey(ctx cosmos.Context, req *types.QueryMimirWithKeyRequest) (*types.QueryMimirWithKeyResponse, error) {
	if len(req.Key) == 0 {
		return nil, fmt.Errorf("no mimir key")
	}

	v, err := qs.mgr.Keeper().GetMimir(ctx, req.Key)
	if err != nil {
		return nil, fmt.Errorf("fail to get mimir with key:%s, err : %w", req.Key, err)
	}
	return &types.QueryMimirWithKeyResponse{
		Value: v,
	}, nil
}

func (qs queryServer) queryMimirValues(ctx cosmos.Context, _ *types.QueryMimirValuesRequest) (*types.QueryMimirValuesResponse, error) {
	resp := types.QueryMimirValuesResponse{
		Mimirs: make([]*types.Mimir, 0),
	}

	// collect all keys with set values, not displaying those with votes but no set value
	keeper := qs.mgr.Keeper()
	iter := keeper.GetMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key := strings.TrimPrefix(string(iter.Key()), "mimir//")
		value, err := keeper.GetMimir(ctx, key)
		if err != nil {
			ctx.Logger().Error("fail to get mimir value", "error", err)
			continue
		}
		if value < 0 {
			ctx.Logger().Error("negative mimir value set", "key", key, "value", value)
			continue
		}
		resp.Mimirs = append(resp.Mimirs, &types.Mimir{
			Key:   key,
			Value: value,
		})
	}

	return &resp, nil
}

func (qs queryServer) queryMimirAdminValues(ctx cosmos.Context, _ *types.QueryMimirAdminValuesRequest) (*types.QueryMimirAdminValuesResponse, error) {
	resp := types.QueryMimirAdminValuesResponse{
		AdminMimirs: make([]*types.Mimir, 0),
	}

	iter := qs.mgr.Keeper().GetMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		value := types.ProtoInt64{}
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &value); err != nil {
			ctx.Logger().Error("fail to unmarshal mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal mimir value: %w", err)
		}
		k := strings.TrimPrefix(string(iter.Key()), "mimir//")
		resp.AdminMimirs = append(resp.AdminMimirs, &types.Mimir{
			Key:   k,
			Value: value.GetValue(),
		})

	}
	return &resp, nil
}

func (qs queryServer) queryMimirNodesAllValues(ctx cosmos.Context, _ *types.QueryMimirNodesAllValuesRequest) (*types.QueryMimirNodesAllValuesResponse, error) {
	resp := types.QueryMimirNodesAllValuesResponse{
		Mimirs: make([]types.NodeMimir, 0),
	}

	iter := qs.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		m := NodeMimirs{}
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &m); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}
		resp.Mimirs = append(resp.Mimirs, m.Mimirs...)
	}

	return &resp, nil
}

func (qs queryServer) queryMimirNodesValues(ctx cosmos.Context, _ *types.QueryMimirNodesValuesRequest) (*types.QueryMimirNodesValuesResponse, error) {
	activeNodes, err := qs.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		ctx.Logger().Error("fail to fetch active node accounts", "error", err)
		return nil, fmt.Errorf("fail to fetch active node accounts: %w", err)
	}
	active := activeNodes.GetNodeAddresses()

	resp := types.QueryMimirNodesValuesResponse{
		Mimirs: make([]*types.Mimir, 0),
	}

	iter := qs.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		mimirs := NodeMimirs{}
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &mimirs); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}
		k := strings.TrimPrefix(string(iter.Key()), "nodemimir//")
		if v, ok := mimirs.HasSuperMajority(k, active); ok {
			resp.Mimirs = append(resp.Mimirs, &types.Mimir{
				Key:   k,
				Value: v,
			})
		}
	}

	return &resp, nil
}

func (qs queryServer) queryMimirNodeValues(ctx cosmos.Context, req *types.QueryMimirNodeValuesRequest) (*types.QueryMimirNodeValuesResponse, error) {
	acc, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to parse thor address", "error", err)
		return nil, fmt.Errorf("fail to parse thor address: %w", err)
	}

	resp := types.QueryMimirNodeValuesResponse{
		NodeMimirs: make([]*types.Mimir, 0),
	}

	iter := qs.mgr.Keeper().GetNodeMimirIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		mimirs := NodeMimirs{}
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &mimirs); err != nil {
			ctx.Logger().Error("fail to unmarshal node mimir v2 value", "error", err)
			return nil, fmt.Errorf("fail to unmarshal node mimir value: %w", err)
		}

		k := strings.TrimPrefix(string(iter.Key()), "nodemimir//")
		if v, ok := mimirs.Get(k, acc); ok {
			resp.NodeMimirs = append(resp.NodeMimirs, &types.Mimir{
				Key:   k,
				Value: v,
			})
		}
	}

	return &resp, nil
}

func (qs queryServer) queryOutboundFees(ctx cosmos.Context, asset string) (*types.QueryOutboundFeesResponse, error) {
	var assets []common.Asset

	if asset != "" {
		// If an Asset has been specified, return information for just that Asset
		// (even if for instance a Derived Asset to show its THORChain outbound fee).
		asset, err := common.NewAsset(asset)
		if err != nil {
			ctx.Logger().Error("fail to parse asset", "error", err, "asset", asset)
			return nil, fmt.Errorf("fail to parse asset (%s): %w", asset, err)
		}
		assets = []common.Asset{asset}
	} else {
		// By default display the outbound fees of RUNE and all external-chain Layer 1 assets.
		// Even Staged pool Assets can incur outbound fees (from withdraw outbounds).
		assets = []common.Asset{common.RuneAsset()}
		iterator := qs.mgr.Keeper().GetPoolIterator(ctx)
		defer iterator.Close()
		for ; iterator.Valid(); iterator.Next() {
			var pool Pool
			if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
				return nil, fmt.Errorf("fail to unmarshal pool: %w", err)
			}

			if pool.Asset.IsNative() {
				// To avoid clutter do not by default display the outbound fees
				// of THORChain Assets other than RUNE.
				continue
			}
			if pool.BalanceAsset.IsZero() || pool.BalanceRune.IsZero() {
				// A Layer 1 Asset's pool must have both depths be non-zero
				// for any outbound fee withholding or gas reimbursement to take place.
				// (This can take place even if the PoolUnits are zero and all liquidity is synths.)
				continue
			}

			assets = append(assets, pool.Asset)
		}
	}

	// Obtain the unchanging CalcOutboundFeeMultiplier arguments before the loop which calls it.
	targetSurplusRune := cosmos.NewUint(uint64(qs.mgr.Keeper().GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune)))
	maxMultiplier := cosmos.NewUint(uint64(qs.mgr.Keeper().GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)))
	minMultiplier := cosmos.NewUint(uint64(qs.mgr.Keeper().GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)))

	// Due to the nature of pool iteration by key, this is expected to have RUNE at the top and then be in alphabetical order.
	result := make([]*types.QueryOutboundFeeResponse, 0, len(assets))
	for i := range assets {
		// Display the Asset's fee as the amount of that Asset deducted.
		outboundFee, err := qs.mgr.GasMgr().GetAssetOutboundFee(ctx, assets[i], false)
		if err != nil {
			ctx.Logger().Error("fail to get asset outbound fee", "asset", assets[i], "error", err)
		}

		// Only display fields other than asset and outbound_fee when the Asset is external,
		// as a non-zero dynamic multiplier could be misleading otherwise.
		var outboundFeeWithheldRuneString, outboundFeeSpentRuneString, surplusRuneString, dynamicMultiplierBasisPointsString string
		if !assets[i].IsNative() {
			outboundFeeWithheldRune, err := qs.mgr.Keeper().GetOutboundFeeWithheldRune(ctx, assets[i])
			if err != nil {
				ctx.Logger().Error("fail to get outbound fee withheld rune", "outbound asset", assets[i], "error", err)
				return nil, fmt.Errorf("fail to get outbound fee withheld rune for asset (%s): %w", assets[i], err)
			}
			outboundFeeWithheldRuneString = outboundFeeWithheldRune.String()

			outboundFeeSpentRune, err := qs.mgr.Keeper().GetOutboundFeeSpentRune(ctx, assets[i])
			if err != nil {
				ctx.Logger().Error("fail to get outbound fee spent rune", "outbound asset", assets[i], "error", err)
				return nil, fmt.Errorf("fail to get outbound fee spent rune for asset (%s): %w", assets[i], err)
			}
			outboundFeeSpentRuneString = outboundFeeSpentRune.String()

			surplusRuneString = common.SafeSub(outboundFeeWithheldRune, outboundFeeSpentRune).String()

			dynamicMultiplierBasisPointsString = qs.mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, targetSurplusRune, outboundFeeSpentRune, outboundFeeWithheldRune, maxMultiplier, minMultiplier).String()
		}

		// As the entire endpoint is for outbounds, the term 'Outbound' is omitted from the field names.
		result = append(result, &types.QueryOutboundFeeResponse{
			Asset:                        assets[i].String(),
			OutboundFee:                  outboundFee.String(),
			FeeWithheldRune:              outboundFeeWithheldRuneString,
			FeeSpentRune:                 outboundFeeSpentRuneString,
			SurplusRune:                  surplusRuneString,
			DynamicMultiplierBasisPoints: dynamicMultiplierBasisPointsString,
		})

	}

	return &types.QueryOutboundFeesResponse{OutboundFees: result}, nil
}

func (qs queryServer) queryBan(ctx cosmos.Context, req *types.QueryBanRequest) (*types.BanVoter, error) {
	if len(req.Address) == 0 {
		return nil, errors.New("node address not available")
	}
	addr, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		ctx.Logger().Error("invalid node address", "error", err)
		return nil, fmt.Errorf("invalid node address: %w", err)
	}

	ban, err := qs.mgr.Keeper().GetBanVoter(ctx, addr)
	if err != nil {
		ctx.Logger().Error("fail to get ban voter", "error", err)
		return nil, fmt.Errorf("fail to get ban voter: %w", err)
	}

	return &ban, nil
}

func (qs queryServer) queryScheduledOutbound(ctx cosmos.Context, _ *types.QueryScheduledOutboundRequest) (*types.QueryOutboundResponse, error) {
	result := make([]*types.QueryTxOutItem, 0)
	constAccessor := qs.mgr.GetConstants()
	maxTxOutOffset, err := qs.mgr.Keeper().GetMimir(ctx, constants.MaxTxOutOffset.String())
	if maxTxOutOffset < 0 || err != nil {
		maxTxOutOffset = constAccessor.GetInt64Value(constants.MaxTxOutOffset)
	}
	for height := ctx.BlockHeight() + 1; height <= ctx.BlockHeight()+17280; height++ {
		txOut, err := qs.mgr.Keeper().GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			continue
		}
		if height > ctx.BlockHeight()+maxTxOutOffset && len(txOut.TxArray) == 0 {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		for _, toi := range txOut.TxArray {
			result = append(result, castTxOutItem(toi, height))
		}
	}

	return &types.QueryOutboundResponse{TxOutItems: result}, nil
}

func (qs queryServer) queryPendingOutbound(ctx cosmos.Context, _ *types.QueryPendingOutboundRequest) (*types.QueryOutboundResponse, error) {
	signingTransactionPeriod := qs.mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	rescheduleCoalesceBlocks := qs.mgr.Keeper().GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks)
	// XMR and EVM token/aggregator outbounds stay live for an extended signing
	// period (see LackSigning); scan that window and apply each item's own period
	// below so they remain visible until they are actually rescheduled.
	startHeight := ctx.BlockHeight() - types.MaxTxOutSigningTransactionPeriod(signingTransactionPeriod)
	if startHeight < 1 {
		startHeight = 1
	}

	// outbounds can be rescheduled to a future height which is the rounded-up nearest multiple of reschedule coalesce blocks
	lastOutboundHeight := ctx.BlockHeight()
	if rescheduleCoalesceBlocks > 1 {
		overBlocks := lastOutboundHeight % rescheduleCoalesceBlocks
		if overBlocks != 0 {
			lastOutboundHeight += rescheduleCoalesceBlocks - overBlocks
		}
	}

	result := make([]*types.QueryTxOutItem, 0)
	for height := startHeight; height <= lastOutboundHeight; height++ {
		txs, err := qs.mgr.Keeper().GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get tx out array from key value store", "error", err)
			return nil, fmt.Errorf("fail to get tx out array from key value store: %w", err)
		}
		for _, tx := range txs.TxArray {
			if tx.OutHash.IsEmpty() && height >= ctx.BlockHeight()-tx.SigningTransactionPeriod(signingTransactionPeriod) {
				result = append(result, castTxOutItem(tx, height))
			}
		}
	}

	return &types.QueryOutboundResponse{TxOutItems: result}, nil
}

func (qs queryServer) querySwapQueue(ctx cosmos.Context, _ *types.QuerySwapQueueRequest) (*types.QuerySwapQueueResponse, error) {
	result := make([]*MsgSwap, 0)
	qs.iterSwapQueue(ctx, func(msg *MsgSwap) { result = append(result, msg) })

	return &types.QuerySwapQueueResponse{SwapQueue: result}, nil
}

func (qs queryServer) querySwapQueuePaginated(ctx cosmos.Context, req *types.QuerySwapQueuePaginatedRequest) (*types.QuerySwapQueuePaginatedResponse, error) {
	offset := req.Offset
	limit := req.Limit
	if limit == 0 {
		limit = config.GetThornode().API.Pagination.DefaultPageSize
	}
	if limit > config.GetThornode().API.Pagination.MaxPageSize {
		limit = config.GetThornode().API.Pagination.MaxPageSize
	}

	result := make([]*MsgSwap, 0)
	var total uint64
	// TODO: If swap queue pagination starts showing performance issues, avoid
	// unmarshalling entries outside the requested page window.
	qs.iterSwapQueue(ctx, func(msg *MsgSwap) {
		if total >= offset && uint64(len(result)) < limit {
			result = append(result, msg)
		}
		total++
	})

	return &types.QuerySwapQueuePaginatedResponse{
		SwapQueue: result,
		Pagination: &types.PaginationMeta{
			Offset:  offset,
			Limit:   limit,
			Total:   total,
			HasNext: total > offset && total-offset > limit,
			HasPrev: offset > 0,
		},
	}, nil
}

func (qs queryServer) iterSwapQueue(ctx cosmos.Context, handle func(*MsgSwap)) {
	// Add items from regular swap queue
	iterator := qs.mgr.Keeper().GetSwapQueueIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		msg := &MsgSwap{}
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), msg); err != nil {
			continue
		}
		handle(msg)
	}

	// Add items from advanced swap queue if enabled
	if qs.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		advIterator := qs.mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		defer advIterator.Close()
		for ; advIterator.Valid(); advIterator.Next() {
			msg := &MsgSwap{}
			if err := qs.mgr.Keeper().Cdc().Unmarshal(advIterator.Value(), msg); err != nil {
				continue
			}
			handle(msg)
		}
	}
}

func (qs queryServer) querySwapDetails(ctx cosmos.Context, req *types.QuerySwapDetailsRequest) (*types.QuerySwapDetailsResponse, error) {
	if len(req.TxId) == 0 {
		return nil, fmt.Errorf("missing tx_id parameter")
	}

	txID, err := common.NewTxID(req.TxId)
	if err != nil {
		return nil, fmt.Errorf("invalid tx_id: %w", err)
	}

	// Check if it's in the regular swap queue
	iterator := qs.mgr.Keeper().GetSwapQueueIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var msg MsgSwap
		if err := qs.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &msg); err != nil {
			continue
		}
		if msg.Tx.ID.Equals(txID) {
			return &types.QuerySwapDetailsResponse{
				Swap:      &msg,
				Status:    SwapStatusQueued,
				QueueType: QueueTypeRegular,
			}, nil
		}
	}

	// Check if it's in the advanced swap queue
	if qs.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		if qs.mgr.Keeper().HasAdvSwapQueueItem(ctx, txID, 0) {
			msg, err := qs.mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
			if err != nil {
				return nil, fmt.Errorf("failed to get advanced swap queue item: %w", err)
			}
			return &types.QuerySwapDetailsResponse{
				Swap:      &msg,
				Status:    SwapStatusQueued,
				QueueType: QueueTypeAdvanced,
			}, nil
		}
	}

	return nil, fmt.Errorf("swap with tx_id %s not found in any queue", req.TxId)
}

func (qs queryServer) queryTssKeygenMetric(ctx cosmos.Context, req *types.QueryTssKeygenMetricRequest) (*types.QueryTssKeygenMetricResponse, error) {
	if len(req.PubKey) == 0 {
		return nil, fmt.Errorf("missing pub_key parameter")
	}
	pkey, err := common.NewPubKey(req.PubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to parse pubkey(%s) err:%w", req.PubKey, err)
	}

	var result []*types.TssKeygenMetric
	m, err := qs.mgr.Keeper().GetTssKeygenMetric(ctx, pkey)
	if err != nil {
		return nil, fmt.Errorf("fail to get tss keygen metric for pubkey(%s):%w", pkey, err)
	}
	result = append(result, m)

	return &types.QueryTssKeygenMetricResponse{Metrics: result}, nil
}

func (qs queryServer) queryTssMetric(ctx cosmos.Context, _ *types.QueryTssMetricRequest) (*types.QueryTssMetricResponse, error) {
	var pubKeys common.PubKeys
	// get all active asgard
	vaults, err := qs.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return nil, fmt.Errorf("fail to get active asgards:%w", err)
	}
	for _, v := range vaults {
		pubKeys = append(pubKeys, v.PubKey)
	}
	var keygenMetrics []*types.TssKeygenMetric
	for _, pkey := range pubKeys {
		var m *types.TssKeygenMetric
		m, err = qs.mgr.Keeper().GetTssKeygenMetric(ctx, pkey)
		if err != nil {
			return nil, fmt.Errorf("fail to get tss keygen metric for pubkey(%s):%w", pkey, err)
		}
		if len(m.NodeTssTimes) == 0 {
			continue
		}
		keygenMetrics = append(keygenMetrics, m)
	}
	keysignMetric, err := qs.mgr.Keeper().GetLatestTssKeysignMetric(ctx)
	if err != nil {
		return nil, fmt.Errorf("fail to get keysign metric:%w", err)
	}

	return &types.QueryTssMetricResponse{
		Keygen:  keygenMetrics,
		Keysign: keysignMetric,
	}, nil
}

func (qs queryServer) queryInvariants(_ cosmos.Context, _ *types.QueryInvariantsRequest) (*types.QueryInvariantsResponse, error) {
	result := types.QueryInvariantsResponse{}
	for _, route := range qs.mgr.Keeper().InvariantRoutes() {
		result.Invariants = append(result.Invariants, route.Route)
	}
	return &result, nil
}

func (qs queryServer) queryInvariant(ctx cosmos.Context, req *types.QueryInvariantRequest) (*types.QueryInvariantResponse, error) {
	if len(req.Path) < 1 {
		return nil, fmt.Errorf("invalid path: %v", req.Path)
	}
	for _, route := range qs.mgr.Keeper().InvariantRoutes() {
		if strings.EqualFold(route.Route, req.Path) {
			msg, broken := route.Invariant(ctx)
			result := types.QueryInvariantResponse{
				Invariant: route.Route,
				Broken:    broken,
				Msg:       msg,
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("invariant not registered: %s", req.Path)
}

func (qs queryServer) queryBlock(ctx cosmos.Context, req *types.QueryBlockRequest) (*types.QueryBlockResponse, error) {
	tmClient, err := getTendermintClient()
	if err != nil {
		return nil, err
	}
	height := ctx.BlockHeight()
	if parsed, parseErr := strconv.ParseInt(req.Height, 10, 64); parseErr == nil {
		height = parsed
	}

	// get the block and results from tendermint rpc
	block, err := tmClient.Block(ctx.Context(), &height)
	if err != nil {
		return nil, fmt.Errorf("fail to get block from tendermint rpc: %w", err)
	}
	results, err := tmClient.BlockResults(ctx.Context(), &height)
	if err != nil {
		return nil, fmt.Errorf("fail to get block results from tendermint rpc: %w", err)
	}

	res := types.QueryBlockResponse{
		Id: &types.BlockResponseId{
			Hash: block.BlockID.Hash.String(),
			Parts: &types.BlockResponseIdParts{
				Total: int64(block.BlockID.PartSetHeader.Total),
				Hash:  block.BlockID.PartSetHeader.Hash.String(),
			},
		},
		Header: &types.BlockResponseHeader{
			Version: &types.BlockResponseHeaderVersion{
				Block: strconv.FormatUint(block.Block.Version.Block, 10),
				App:   strconv.FormatUint(block.Block.Version.App, 10),
			},
			ChainId: block.Block.ChainID,
			Height:  block.Block.Height,
			Time:    block.Block.Time.Format(time.RFC3339Nano),
			LastBlockId: &types.BlockResponseId{
				Hash: block.Block.LastBlockID.Hash.String(),
				Parts: &types.BlockResponseIdParts{
					Total: int64(block.Block.LastBlockID.PartSetHeader.Total),
					Hash:  block.Block.LastBlockID.PartSetHeader.Hash.String(),
				},
			},
			LastCommitHash:     block.Block.LastCommitHash.String(),
			DataHash:           block.Block.DataHash.String(),
			ValidatorsHash:     block.Block.ValidatorsHash.String(),
			NextValidatorsHash: block.Block.NextValidatorsHash.String(),
			ConsensusHash:      block.Block.ConsensusHash.String(),
			AppHash:            block.Block.AppHash.String(),
			LastResultsHash:    block.Block.LastResultsHash.String(),
			EvidenceHash:       block.Block.EvidenceHash.String(),
			ProposerAddress:    block.Block.ProposerAddress.String(),
		},
		Txs: make([]*types.QueryBlockTx, len(block.Block.Txs)),
	}

	// parse the events
	for _, event := range results.FinalizeBlockEvents {
		foundMode := false
		for _, attr := range event.Attributes {
			if attr.Key == "mode" {
				if attr.Value == "BeginBlock" {
					res.BeginBlockEvents = append(res.BeginBlockEvents, blockEvent(sdk.Event(event)))
					foundMode = true
				}
				if attr.Value == "EndBlock" {
					res.EndBlockEvents = append(res.EndBlockEvents, blockEvent(sdk.Event(event)))
					foundMode = true
				}
				continue
			}
		}
		if !foundMode {
			res.FinalizeBlockEvents = append(res.FinalizeBlockEvents, blockEvent(sdk.Event(event)))
		}
	}

	for i, tx := range block.Block.Txs {
		// decode the protobuf and encode to json

		dtx, err := qs.txConfig.TxDecoder()(tx)
		if err != nil {
			return nil, fmt.Errorf("fail to decode tx: %w", err)
		}

		etx, err := qs.txConfig.TxJSONEncoder()(dtx)
		if err != nil {
			return nil, fmt.Errorf("fail to encode tx: %w", err)
		}

		resultTx := results.TxsResults[i]

		// Attempt to unmarshal the tx result's data, if it of type MsgEmpty, don't include it as it's not useful
		var emptyMsg types.MsgEmpty
		err = qs.mgr.cdc.UnmarshalInterface(resultTx.Data, &emptyMsg)
		if err == nil {
			resultTx.Data = nil
		}

		res.Txs[i] = &types.QueryBlockTx{
			Tx:   etx,
			Hash: strings.ToUpper(hex.EncodeToString(tx.Hash())),
			Result: &types.BlockTxResult{
				Code:      int64(resultTx.Code),
				Data:      string(resultTx.Data),
				Log:       resultTx.Log,
				Info:      resultTx.Info,
				GasWanted: strconv.FormatInt(resultTx.GasWanted, 10),
				GasUsed:   strconv.FormatInt(resultTx.GasUsed, 10),
				Events:    make([]*types.BlockEvent, len(resultTx.Events)),
			},
		}

		for j, event := range resultTx.Events {
			res.Txs[i].Result.Events[j] = blockEvent(sdk.Event(event))
		}
	}

	return &res, nil
}

// -------------------------------------------------------------------------------------
// Generic Helpers
// -------------------------------------------------------------------------------------

func castTxOutItem(toi TxOutItem, height int64) *types.QueryTxOutItem {
	return &types.QueryTxOutItem{
		Height:                height, // Omitted if 0, for use in openapi.TxDetailsResponse
		VaultPubKey:           toi.VaultPubKey.String(),
		VaultPubKeyEddsa:      toi.VaultPubKeyEddsa.String(),
		VaultPubKeyFrost:      toi.VaultPubKeyFrost.String(),
		InHash:                toi.InHash.String(),
		OutHash:               toi.OutHash.String(),
		Chain:                 toi.Chain.String(),
		ToAddress:             toi.ToAddress.String(),
		Coin:                  &toi.Coin,
		MaxGas:                toi.MaxGas,
		GasRate:               toi.GasRate,
		Memo:                  toi.Memo,
		OriginalMemo:          toi.OriginalMemo,
		Aggregator:            toi.Aggregator,
		AggregatorTargetAsset: toi.AggregatorTargetAsset,
		AggregatorTargetLimit: toi.AggregatorTargetLimit.String(),
		CloutSpent:            toi.CloutSpent.String(),
	}
}

func castObservedTx(observedTx ObservedTx) types.QueryObservedTx {
	// Only display the Status if it is "done", not if "incomplete".
	status := ""
	if observedTx.Status != common.Status_incomplete {
		status = observedTx.Status.String()
	}
	return types.QueryObservedTx{
		Tx:                    observedTx.Tx,
		Status:                status,
		OutHashes:             observedTx.OutHashes,
		BlockHeight:           observedTx.BlockHeight,
		Signers:               observedTx.Signers,
		ObservedPubKey:        observedTx.ObservedPubKey,
		KeysignMs:             observedTx.KeysignMs,
		FinaliseHeight:        observedTx.FinaliseHeight,
		Aggregator:            observedTx.Aggregator,
		AggregatorTarget:      observedTx.AggregatorTarget,
		AggregatorTargetLimit: observedTx.AggregatorTargetLimit,
		SpentOutputRefs:       castOutputRefs(observedTx.SpentOutputRefs),
		XmrTxKey:              observedTx.XmrTxKey,
		XmrMinedHeight:        observedTx.XmrMinedHeight,
	}
}

func castOutputRefs(refs []common.OutputRef) []*common.OutputRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]*common.OutputRef, 0, len(refs))
	for i := range refs {
		out = append(out, &refs[i])
	}
	return out
}

func castVaultRouters(chainContracts []ChainContract) []*types.VaultRouter {
	// Leave this nil (null rather than []) if the source is nil.
	if chainContracts == nil {
		return nil
	}

	routers := make([]*types.VaultRouter, len(chainContracts))
	for i := range chainContracts {
		routers[i] = &types.VaultRouter{
			Chain:  chainContracts[i].Chain.String(),
			Router: chainContracts[i].Router.String(),
		}
	}
	return routers
}

func blockEvent(e sdk.Event) *types.BlockEvent {
	event := types.BlockEvent{}
	event.EventKvPair = append(event.EventKvPair, &types.EventKeyValuePair{
		Key:   "type",
		Value: e.Type,
	})

	for _, a := range e.Attributes {
		event.EventKvPair = append(event.EventKvPair, &types.EventKeyValuePair{
			Key:   a.Key,
			Value: a.Value,
		})
	}
	return &event
}

func eventMap(e sdk.Event) map[string]string {
	m := map[string]string{}
	m["type"] = e.Type
	for _, a := range e.Attributes {
		m[a.Key] = a.Value
	}
	return m
}

func simulate(ctx cosmos.Context, mgr Manager, msg sdk.Msg) (sdk.Events, error) {
	// use the first active node account as the signer (same store access as
	// ListActiveValidators, without unmarshalling the other active nodes)
	nodeAccounts, err := mgr.Keeper().ListActiveValidatorsUpTo(ctx, 1)
	if err != nil {
		return nil, fmt.Errorf("no active node accounts: %w", err)
	}

	// set the signer
	if m, ok := msg.(*MsgSwap); ok {
		m.Signer = nodeAccounts[0].NodeAddress
	}

	// mark context as simulation mode for swap logic
	ctx = ctx.WithValue(constants.CtxSimulationMode, true)

	// validate
	msgV, ok := msg.(sdk.HasValidateBasic)
	if !ok {
		return nil, fmt.Errorf("message doesn't support validation")
	}
	err = msgV.ValidateBasic()
	if err != nil {
		return nil, fmt.Errorf("failed to validate message: %w", err)
	}

	// TODO: evaluate if this CacheMultiStore is still needed given
	//   that the querier is called with a CacheContext.
	// intercept events and avoid modifying state
	cms := ctx.MultiStore().CacheMultiStore() // never call cms.Write()
	em := cosmos.NewEventManager()
	ctx = ctx.WithMultiStore(cms).WithEventManager(em)
	// cms is discarded, so its txout writes need not disable the caller's
	// txout cache (if any) for the caller's later simulations
	ctx = keeper.ForkTxOutCache(ctx)

	// disable logging
	ctx = ctx.WithLogger(nullLogger)

	// reset the swap queues (both regular and advanced)
	iter := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		mgr.Keeper().DeleteKey(ctx, iter.Key())
	}
	iter.Close()

	// AdvSwapQueueEnabled reads a config/mimir value that does not change during
	// simulation, so resolve it once instead of on every end-block iteration.
	advSwapQueueEnabled := mgr.Keeper().AdvSwapQueueEnabled(ctx)

	// reset the advanced swap queue if enabled
	if advSwapQueueEnabled {
		advIter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advIter.Valid(); advIter.Next() {
			mgr.Keeper().DeleteKey(ctx, advIter.Key())
		}
		advIter.Close()
	}

	// save pool state - the end-block loop restores these between iterations so
	// each simulated sub-swap of a streaming swap sees the original pool depth
	pools, err := mgr.Keeper().GetPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pools: %w", err)
	}
	cdc := mgr.Keeper().Cdc()

	// simulate the handler
	_, err = NewInternalHandler(mgr)(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("failed to simulate handler: %w", err)
	}

	// touchedPools is the set of pools mutated while processing this swap. It is
	// detected on the first end-block iteration by diffing against the snapshot
	// and reused thereafter: simulate() only ever processes a single MsgSwap, so
	// every streaming sub-swap follows the same route and touches the same pools.
	// Restoring only these (rather than every pool, every iteration) keeps the
	// per-iteration restore O(route) instead of O(all pools).
	var touchedPools []Pool
	touchedDetected := false

	// simulate end block, loop it until the swap queue is empty
	queueEmpty := false
	for count := int64(0); !queueEmpty && count < 1000; count += 1 {
		blockCtx := ctx.WithBlockHeight(ctx.BlockHeight() + count)

		// process regular swap queue
		err = mgr.SwapQ().EndBlock(blockCtx, mgr)
		if err != nil {
			return nil, fmt.Errorf("failed to simulate end block for regular queue: %w", err)
		}

		// process advanced swap queue if enabled
		if advSwapQueueEnabled {
			err = mgr.AdvSwapQueueMgr().EndBlock(blockCtx, mgr, false)
			if err != nil {
				return nil, fmt.Errorf("failed to simulate end block for advanced queue: %w", err)
			}
		}

		// restore pool depths to the pre-swap snapshot. On the first iteration
		// the touched set is unknown, so diff every pool against the snapshot;
		// thereafter only the pools the swap actually moved need restoring.
		if !touchedDetected {
			for i := range pools {
				var current Pool
				current, err = mgr.Keeper().GetPool(ctx, pools[i].Asset)
				if err != nil {
					return nil, fmt.Errorf("failed to get pool: %w", err)
				}
				if !bytes.Equal(cdc.MustMarshal(&current), cdc.MustMarshal(&pools[i])) {
					touchedPools = append(touchedPools, pools[i])
				}
			}
			touchedDetected = true
		}
		for _, pool := range touchedPools {
			_ = mgr.Keeper().SetPool(ctx, pool)
		}

		// check both regular and advanced swap queues for emptiness
		iter = mgr.Keeper().GetSwapQueueIterator(ctx)
		regularQueueEmpty := !iter.Valid()
		iter.Close()

		advQueueEmpty := true
		if advSwapQueueEnabled {
			advIter := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
			advQueueEmpty = !advIter.Valid()
			advIter.Close()
		}

		queueEmpty = regularQueueEmpty && advQueueEmpty
	}

	return em.Events(), nil
}

// runePerDollarIgnoreHalt mirrors keeper.RunePerDollar, ignoring halts by using
// dollarsPerRuneIgnoreHalt to return the last known price instead of "0"
func runePerDollarIgnoreHalt(ctx cosmos.Context, k keeper.Keeper) cosmos.Uint {
	runePerDollar := dollarsPerRuneIgnoreHalt(ctx, k)

	one := cosmos.NewUint(common.One)

	return common.GetUncappedShare(one, runePerDollar, one)
}

// dollarsPerRuneIgnoreHalt mirrors keeper.DollarsPerRune, but ignoring halts if all
// anchor chains are unavailable with them. This is used for the TOR price on pools to
// ensure a best effort price is returned whenever possible instead of zero.
func dollarsPerRuneIgnoreHalt(ctx cosmos.Context, k keeper.Keeper) cosmos.Uint {
	// check for mimir override
	dollarsPerRune, err := k.GetMimir(ctx, "DollarsPerRune")
	if err == nil && dollarsPerRune > 0 {
		return cosmos.NewUint(uint64(dollarsPerRune))
	}

	usdAssets := k.GetAnchors(ctx, common.TOR)

	// if all anchor chains have trading halt, then ignore trading halt
	ignoreHalt := true
	for _, asset := range usdAssets {
		if !k.IsChainTradingHalted(ctx, asset.Chain) {
			ignoreHalt = false
			break
		}
	}

	p := make([]cosmos.Uint, 0)
	for _, asset := range usdAssets {
		if !ignoreHalt && k.IsChainTradingHalted(ctx, asset.Chain) {
			continue
		}
		pool, err := k.GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("fail to get usd pool", "asset", asset.String(), "error", err)
			continue
		}
		if !pool.IsAvailable() {
			continue
		}
		// value := common.GetUncappedShare(pool.BalanceAsset, pool.BalanceRune, cosmos.NewUint(common.One))
		value := pool.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti * common.One))

		if !value.IsZero() {
			p = append(p, value)
		}
	}
	return common.GetMedianUint(p).QuoUint64(constants.DollarMulti)
}

// queryTCYStakers
func (qs queryServer) queryTCYStakers(ctx cosmos.Context, req *types.QueryTCYStakersRequest) (*types.QueryTCYStakersResponse, error) {
	var stakers []*types.QueryTCYStakerResponse
	tcyStakers, err := qs.mgr.Keeper().ListTCYStakers(ctx)
	if err != nil {
		return &types.QueryTCYStakersResponse{}, err
	}
	for _, staker := range tcyStakers {
		stakers = append(stakers, &types.QueryTCYStakerResponse{
			Address: staker.Address.String(),
			Amount:  staker.Amount.String(),
		})
	}
	return &types.QueryTCYStakersResponse{TcyStakers: stakers}, nil
}

// queryTCYStaker
func (qs queryServer) queryTCYStaker(ctx cosmos.Context, req *types.QueryTCYStakerRequest) (*types.QueryTCYStakerResponse, error) {
	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	staker, err := qs.mgr.Keeper().GetTCYStaker(ctx, addr)
	if err != nil {
		ctx.Logger().Error("fail to get tcy staker", "error", err)
		return nil, fmt.Errorf("fail to tcy staker: %w", err)
	}

	stakerRes := types.QueryTCYStakerResponse{
		Address: staker.Address.String(),
		Amount:  staker.Amount.String(),
	}

	return &stakerRes, nil
}

// queryTCYClaimers
func (qs queryServer) queryTCYClaimers(ctx cosmos.Context, req *types.QueryTCYClaimersRequest) (*types.QueryTCYClaimersResponse, error) {
	var claimers []*types.QueryTCYClaimer
	iterator := qs.mgr.Keeper().GetTCYClaimerIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var claimer TCYClaimer
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &claimer)

		claimers = append(claimers, &types.QueryTCYClaimer{
			Asset:     claimer.Asset.String(),
			L1Address: claimer.L1Address.String(),
			Amount:    claimer.Amount.String(),
		})
	}
	return &types.QueryTCYClaimersResponse{TcyClaimers: claimers}, nil
}

// queryTCYClaimer
func (qs queryServer) queryTCYClaimer(ctx cosmos.Context, req *types.QueryTCYClaimerRequest) (*types.QueryTCYClaimerResponse, error) {
	addr, err := common.NewAddress(req.Address)
	if err != nil {
		ctx.Logger().Error("fail to get parse address", "error", err)
		return nil, fmt.Errorf("fail to parse address: %w", err)
	}
	addressClaims, err := qs.mgr.Keeper().ListTCYClaimersFromL1Address(ctx, addr)
	if err != nil {
		ctx.Logger().Error("fail to get tcy claimer", "error", err)
		return nil, fmt.Errorf("fail to tcy claimer: %w", err)
	}

	var claimsRes []*types.QueryTCYClaimer
	for _, claim := range addressClaims {
		claimsRes = append(claimsRes, &types.QueryTCYClaimer{
			Asset:     claim.Asset.String(),
			L1Address: claim.L1Address.String(),
			Amount:    claim.Amount.String(),
		})
	}

	return &types.QueryTCYClaimerResponse{TcyClaimer: claimsRes}, nil
}

// queryTCYProtocolOwned returns the amount of TCY held by the claim module
// in excess of the outstanding TCYClaimer entitlements. This surplus is
// accumulated by claimingSwapRuneToTCY each distribution cycle: RUNE rewards
// earmarked for the claim module's own TCY balance (plus the pool's TCY
// depth and unaccounted supply) are swapped back into TCY against the pool,
// and the resulting TCY accumulates in the claim module on top of the
// unclaimed-entitlement backing.
func (qs queryServer) queryTCYProtocolOwned(ctx cosmos.Context, _ *types.QueryTCYProtocolOwnedRequest) (*types.QueryTCYProtocolOwnedResponse, error) {
	claimBalance := qs.mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())

	unclaimed := cosmos.ZeroUint()
	iterator := qs.mgr.Keeper().GetTCYClaimerIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var claimer TCYClaimer
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &claimer)
		unclaimed = unclaimed.Add(claimer.Amount)
	}

	protocolOwned := common.SafeSub(claimBalance, unclaimed)
	return &types.QueryTCYProtocolOwnedResponse{Amount: protocolOwned.String()}, nil
}

// queryOraclePrices
func (qs queryServer) queryOraclePrices(ctx cosmos.Context, _ *types.QueryOraclePricesRequest) (*types.QueryOraclePricesResponse, error) {
	var prices []*OraclePrice

	iterator := qs.mgr.Keeper().GetPriceIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var price OraclePrice
		qs.mgr.Keeper().Cdc().MustUnmarshal(iterator.Value(), &price)
		prices = append(prices, &price)
	}

	sort.Slice(prices, func(i, j int) bool {
		return prices[i].Symbol < prices[j].Symbol
	})

	return &types.QueryOraclePricesResponse{Prices: prices}, nil
}

// queryOraclePrice
func (qs queryServer) queryOraclePrice(ctx cosmos.Context, req *types.QueryOraclePriceRequest) (*types.QueryOraclePriceResponse, error) {
	price, err := qs.mgr.Keeper().GetPrice(ctx, req.Symbol)
	if err != nil {
		return nil, fmt.Errorf("fail to get price for symbol '%s': %w", req.Symbol, err)
	}

	return &types.QueryOraclePriceResponse{Price: &price}, nil
}

func (qs queryServer) queryEip712TypedData(_ cosmos.Context, req *types.QueryEip712TypedDataRequest) (*types.QueryEip712TypedDataResponse, error) {
	typedData, err := eip712.GetEIP712TypedDataForMsg(req.SignBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to EIP-712 typed data: %w", err)
	}

	// Convert to JSON for output
	data, err := json.Marshal(typedData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal EIP-712 typed data: %w", err)
	}
	return &types.QueryEip712TypedDataResponse{TypedData: string(data)}, nil
}

// querySupply returns the RUNE supply breakdown.
func (qs queryServer) querySupply(ctx cosmos.Context, req *types.QuerySupplyRequest) (*types.QuerySupplyResponse, error) {
	keeper := qs.mgr.Keeper()

	// total RUNE supply from bank module
	runeSupply := keeper.GetTotalSupply(ctx, common.RuneAsset())

	// reserve module balance (locked/non-circulating)
	reserveBal := keeper.GetRuneBalanceOfModule(ctx, ReserveName)

	// circulating = total - reserves
	circulating := common.SafeSub(runeSupply, reserveBal)

	const e8 = 1e8
	return &types.QuerySupplyResponse{
		Circulating: int64(circulating.Uint64() / e8),
		Locked: &types.LockedSupply{
			Reserve: int64(reserveBal.Uint64() / e8),
		},
		Total: int64(runeSupply.Uint64() / e8),
	}, nil
}

func (qs queryServer) queryContractInfo(ctx cosmos.Context, req *types.QueryContractInfoRequest) (*types.QueryContractInfoResponse, error) {
	address, err := cosmos.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	if qs.mgr.wasmKeeper.GetContractInfo(ctx, address) == nil {
		return nil, fmt.Errorf("no wasm contract at address %s", req.Address)
	}

	data := qs.mgr.wasmKeeper.QueryRaw(ctx, address, []byte("contract_info"))
	if len(data) == 0 {
		return nil, fmt.Errorf("contract does not expose contract_info")
	}

	var info ContractInfo

	err = json.Unmarshal(data, &info)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal contract info: %w", err)
	}

	info.Address = req.Address

	return &types.QueryContractInfoResponse{Info: &info}, nil
}

func (qs queryServer) queryContractInfos(ctx cosmos.Context, req *types.QueryContractInfosRequest) (*types.QueryContractInfosResponse, error) {
	var infos []*ContractInfo

	qs.mgr.wasmKeeper.IterateContractInfo(ctx, func(address sdk.AccAddress, _ wasm.ContractInfo) bool {
		data := qs.mgr.wasmKeeper.QueryRaw(ctx, address, []byte("contract_info"))
		if len(data) == 0 {
			return false
		}

		var info ContractInfo

		err := json.Unmarshal(data, &info)
		if err != nil {
			log.Debug().Err(err).Msg("failed to unmarshal contract info")
			return false
		}

		if !strings.HasPrefix(info.Contract, req.Contract) {
			return false
		}

		if !strings.HasPrefix(info.Version, req.Version) {
			return false
		}

		info.Address = address.String()

		infos = append(infos, &info)

		return false
	})

	return &types.QueryContractInfosResponse{Infos: infos}, nil
}
