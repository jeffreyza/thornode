package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SolvencyCheckActor
////////////////////////////////////////////////////////////////////////////////////////

type SolvencyCheckActor struct {
	Actor
}

var xmrSolvencyTolerance = cosmos.NewUint(25)

const (
	solvencyRetryAttempts = 36
	solvencyRetryDelay    = 5 * time.Second
)

type solvencyMismatch struct {
	asset             common.Asset
	assetStr          string
	vaultAddrs        []string
	vaultPubKeys      []string
	vaultPubKeysEddsa []string
	vaultAmount       cosmos.Uint
	l1Amount          cosmos.Uint
	diff              cosmos.Uint
	tolerance         cosmos.Uint
}

func NewSolvencyCheckActor(rng *rand.Rand) *Actor {
	a := &SolvencyCheckActor{
		Actor: *NewActor("SolvencyCheck", rng),
	}

	a.Ops = append(a.Ops, a.checkSolvency)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func solvencyShortfall(vaultAmount, l1Amount cosmos.Uint) cosmos.Uint {
	if l1Amount.GTE(vaultAmount) {
		return cosmos.ZeroUint()
	}
	return vaultAmount.Sub(l1Amount)
}

func solvencyExcess(vaultAmount, l1Amount cosmos.Uint) cosmos.Uint {
	if vaultAmount.GTE(l1Amount) {
		return cosmos.ZeroUint()
	}
	return l1Amount.Sub(vaultAmount)
}

func solvencyTolerance(asset common.Asset) cosmos.Uint {
	if asset.Equals(common.XMRAsset) {
		return xmrSolvencyTolerance
	}
	return cosmos.ZeroUint()
}

func vaultIsSolvent(asset common.Asset, vaultAmount, l1Amount cosmos.Uint) (bool, cosmos.Uint, cosmos.Uint) {
	diff := solvencyShortfall(vaultAmount, l1Amount)
	tolerance := solvencyTolerance(asset)
	return diff.LTE(tolerance), diff, tolerance
}

func solvencyMismatchRetryable(mismatches []solvencyMismatch) bool {
	if len(mismatches) == 0 {
		return false
	}
	for _, mismatch := range mismatches {
		if !mismatch.asset.Equals(common.XMRAsset) && !mismatch.asset.Equals(common.SOLAsset) {
			return false
		}
	}
	return true
}

// ed25519SolvencyPubKey returns the vault pubkey holding the asset's L1 funds. XMR
// monovaults keep their FROST group key in pub_key_frost and leave pub_key_eddsa empty.
func ed25519SolvencyPubKey(asset common.Asset, vaultPubKeyEddsa, vaultPubKeyFrost common.PubKey) common.PubKey {
	if asset.Chain.Equals(common.XMRChain) {
		return vaultPubKeyFrost
	}
	return vaultPubKeyEddsa
}

func skipSolvencyVaultCoin(asset common.Asset, vaultPubKeyEddsa, vaultPubKeyFrost common.PubKey) bool {
	return asset.Equals(common.XMRAsset) && ed25519SolvencyPubKey(asset, vaultPubKeyEddsa, vaultPubKeyFrost).IsEmpty()
}

func appendUniqueString(items []string, item string) []string {
	if item == "" {
		return items
	}
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func addToUintMap(items map[string]cosmos.Uint, key string, amount cosmos.Uint) {
	current, ok := items[key]
	if !ok {
		current = cosmos.ZeroUint()
	}
	items[key] = current.Add(amount)
}

func (a *SolvencyCheckActor) checkSolvency(config *OpConfig) OpResult {
	// wait a moment for any blocks to be created
	time.Sleep(time.Second * 5)

	// find a user to lookup L1 balances
	var user *User
	userAcquired := false
	for _, user = range config.Users {
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())

		// skip users already being used
		if user.Acquire() {
			userAcquired = true
			break
		}
	}
	if !userAcquired {
		a.Log().Error().Msg("no user available")
		return OpResult{
			Continue: false,
		}
	}
	defer user.Release()

	for attempt := 1; attempt <= solvencyRetryAttempts; attempt++ {
		// get all vaults
		vaults, err := thornode.GetVaults()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get vaults")
			return OpResult{
				Continue: false,
			}
		}
		vaultCoinAmounts := make(map[string]cosmos.Uint)
		for _, vault := range vaults {
			vaultPubKeyEddsa, err := common.NewPubKey(vault.GetPubKeyEddsa())
			if err != nil && vault.GetPubKeyEddsa() != "" {
				a.Log().Error().Err(err).Str("pubkey_eddsa", vault.GetPubKeyEddsa()).Msg("failed to parse vault eddsa pubkey")
				return OpResult{Continue: false}
			}
			vaultPubKeyFrost, err := common.NewPubKey(vault.GetPubKeyFrost())
			if err != nil && vault.GetPubKeyFrost() != "" {
				a.Log().Error().Err(err).Str("pubkey_frost", vault.GetPubKeyFrost()).Msg("failed to parse vault frost pubkey")
				return OpResult{Continue: false}
			}
			for _, coin := range vault.Coins {
				asset, err := common.NewAsset(coin.Asset)
				if err != nil {
					a.Log().Fatal().Err(err).Msg("failed to parse asset")
				}
				if skipSolvencyVaultCoin(asset, vaultPubKeyEddsa, vaultPubKeyFrost) {
					a.Log().Warn().Str("asset", coin.Asset).Msg("skipping XMR vault coin without ed25519 pubkey")
					continue
				}
				if _, ok := vaultCoinAmounts[coin.Asset]; !ok {
					vaultCoinAmounts[coin.Asset] = cosmos.ZeroUint()
				}
				vaultCoinAmounts[coin.Asset] = vaultCoinAmounts[coin.Asset].Add(cosmos.NewUintFromString(coin.Amount))
			}
		}

		// get all L1 balances
		l1CoinAmounts := make(map[string]cosmos.Uint)
		vaultAssets := make(map[string]common.Asset)
		vaultAddrs := make(map[string][]string)
		vaultPubKeys := make(map[string][]string)
		vaultPubKeysEddsa := make(map[string][]string)
		l1BalanceSeen := make(map[string]struct{})
		for _, vault := range vaults {
			vaultPubKeyStr := vault.GetPubKey()
			vaultPubKeyEddsaStr := vault.GetPubKeyEddsa()

			var vaultPubKey common.PubKey
			if vaultPubKeyStr != "" {
				vaultPubKey, err = common.NewPubKey(vaultPubKeyStr)
				if err != nil {
					a.Log().Error().Err(err).Str("pubkey", vaultPubKeyStr).Msg("failed to parse vault pubkey")
					return OpResult{Continue: false}
				}
			}

			var vaultPubKeyEddsa common.PubKey
			if vaultPubKeyEddsaStr != "" {
				vaultPubKeyEddsa, err = common.NewPubKey(vaultPubKeyEddsaStr)
				if err != nil {
					a.Log().Error().Err(err).Str("pubkey_eddsa", vaultPubKeyEddsaStr).Msg("failed to parse vault eddsa pubkey")
					return OpResult{Continue: false}
				}
			}

			vaultPubKeyFrostStr := vault.GetPubKeyFrost()
			var vaultPubKeyFrost common.PubKey
			if vaultPubKeyFrostStr != "" {
				vaultPubKeyFrost, err = common.NewPubKey(vaultPubKeyFrostStr)
				if err != nil {
					a.Log().Error().Err(err).Str("pubkey_frost", vaultPubKeyFrostStr).Msg("failed to parse vault frost pubkey")
					return OpResult{Continue: false}
				}
			}

			for _, coin := range vault.Coins {
				assetStr := coin.Asset
				asset, err := common.NewAsset(assetStr)
				if err != nil {
					a.Log().Fatal().Err(err).Msg("failed to parse asset")
				}
				vaultAssets[assetStr] = asset
				if skipSolvencyVaultCoin(asset, vaultPubKeyEddsa, vaultPubKeyFrost) {
					continue
				}

				switch {
				case asset.IsGasAsset():
					switch asset.Chain.GetSigningAlgo() {
					case common.SigningAlgoEd25519:
						algoPubKey := ed25519SolvencyPubKey(asset, vaultPubKeyEddsa, vaultPubKeyFrost)
						queryKey := algoPubKey.String()
						if algoPubKey.IsEmpty() {
							a.Log().Error().Str("asset", assetStr).Msg("missing ed25519 vault pubkey")
							return OpResult{Continue: false}
						}
						seenKey := assetStr + "|" + queryKey
						if _, seen := l1BalanceSeen[seenKey]; seen {
							continue
						}
						l1BalanceSeen[seenKey] = struct{}{}

						vaultAddr, err := algoPubKey.GetAddress(asset.Chain)
						if err != nil {
							a.Log().Error().Err(err).Msg("failed to get vault address for ed25519 pubkey")
							return OpResult{Continue: false}
						}
						vaultAddrs[assetStr] = appendUniqueString(vaultAddrs[assetStr], vaultAddr.String())
						vaultPubKeysEddsa[assetStr] = appendUniqueString(vaultPubKeysEddsa[assetStr], algoPubKey.String())
						l1Acct, err := user.ChainClients[asset.Chain].GetAccount(&algoPubKey)
						if err != nil {
							a.Log().Error().Err(err).Msg("failed to get L1 account for ed25519 pubkey")
							return OpResult{Continue: false}
						}
						addToUintMap(l1CoinAmounts, assetStr, l1Acct.Coins.GetCoin(asset).Amount)
					case common.SigningAlgoSecp256k1:
						if vaultPubKey.IsEmpty() {
							a.Log().Error().Str("asset", assetStr).Msg("missing secp256k1 vault pubkey")
							return OpResult{Continue: false}
						}
						seenKey := assetStr + "|" + vaultPubKey.String()
						if _, seen := l1BalanceSeen[seenKey]; seen {
							continue
						}
						l1BalanceSeen[seenKey] = struct{}{}

						vaultAddr, err := vaultPubKey.GetAddress(asset.Chain)
						if err != nil {
							a.Log().Error().Err(err).Msg("failed to get vault address for secp256k1 pubkey")
							return OpResult{Continue: false}
						}
						vaultAddrs[assetStr] = appendUniqueString(vaultAddrs[assetStr], vaultAddr.String())
						vaultPubKeys[assetStr] = appendUniqueString(vaultPubKeys[assetStr], vaultPubKey.String())
						l1Acct, err := user.ChainClients[asset.Chain].GetAccount(&vaultPubKey)
						if err != nil {
							a.Log().Error().Err(err).Msg("failed to get L1 account")
							return OpResult{Continue: false}
						}
						addToUintMap(l1CoinAmounts, assetStr, l1Acct.Coins.GetCoin(asset).Amount)
					default:
						a.Log().Fatal().Msgf("unsupported signing algo %s for chain %s", asset.Chain.GetSigningAlgo(), asset.Chain)
					}
				case asset.Chain.IsEVM():
					if vaultPubKey.IsEmpty() {
						a.Log().Error().Str("asset", assetStr).Msg("missing vault pubkey for EVM asset")
						return OpResult{Continue: false}
					}
					seenKey := assetStr + "|" + vaultPubKey.String()
					if _, seen := l1BalanceSeen[seenKey]; seen {
						continue
					}
					l1BalanceSeen[seenKey] = struct{}{}

					_, routerAddr, err := thornode.GetInboundAddress(asset.Chain)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get router address")
						return OpResult{Continue: false}
					}
					vaultAddr, err := vaultPubKey.GetAddress(asset.Chain)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get vault address")
						return OpResult{Continue: false}
					}
					vaultAddrs[assetStr] = appendUniqueString(vaultAddrs[assetStr], vaultAddr.String())
					vaultPubKeys[assetStr] = appendUniqueString(vaultPubKeys[assetStr], vaultPubKey.String())
					balance, err := user.ChainClients[asset.Chain].(*evm.Client).GetVaultAllowance(*routerAddr, vaultAddr, asset)
					if err != nil {
						a.Log().Error().Err(err).Msg("failed to get vault allowance")
						return OpResult{Continue: false}
					}
					addToUintMap(l1CoinAmounts, assetStr, balance)
				default:
					a.Log().Error().Msgf("unsupported asset %s", asset)
					return OpResult{
						Continue: false,
						Error:    fmt.Errorf("unsupported asset %s", asset),
					}
				}
			}
		}

		// verify vault and on chain amount are equal for all assets
		mismatches := make([]solvencyMismatch, 0)
		for assetStr, vaultAmount := range vaultCoinAmounts {
			l1Amount, ok := l1CoinAmounts[assetStr]
			if !ok {
				a.Log().Error().Str("asset", assetStr).Msg("L1 amount not found for asset")
			}
			asset := vaultAssets[assetStr]
			solvent, shortfall, tolerance := vaultIsSolvent(asset, vaultAmount, l1Amount)
			if !solvent {
				mismatches = append(mismatches, solvencyMismatch{
					asset:             asset,
					assetStr:          assetStr,
					vaultAddrs:        vaultAddrs[assetStr],
					vaultPubKeys:      vaultPubKeys[assetStr],
					vaultPubKeysEddsa: vaultPubKeysEddsa[assetStr],
					vaultAmount:       vaultAmount,
					l1Amount:          l1Amount,
					diff:              shortfall,
					tolerance:         tolerance,
				})
				continue
			}
			if excess := solvencyExcess(vaultAmount, l1Amount); !excess.IsZero() {
				a.Log().Info().
					Str("asset", assetStr).
					Strs("vault_addresses", vaultAddrs[assetStr]).
					Str("vault_amount", vaultAmount.String()).
					Str("l1_amount", l1Amount.String()).
					Str("difference", excess.String()).
					Msg("L1 amount exceeds vault amount, vault remains solvent")
				continue
			}
			if !shortfall.IsZero() {
				a.Log().Warn().
					Str("asset", assetStr).
					Strs("vault_addresses", vaultAddrs[assetStr]).
					Str("vault_amount", vaultAmount.String()).
					Str("l1_amount", l1Amount.String()).
					Str("difference", shortfall.String()).
					Str("tolerance", tolerance.String()).
					Msg("vault and L1 amounts differ within tolerance")
			}
		}
		if len(mismatches) == 0 {
			return OpResult{
				Continue: true,
			}
		}

		retryable := attempt < solvencyRetryAttempts && solvencyMismatchRetryable(mismatches)
		for _, mismatch := range mismatches {
			logMismatch := a.Log().Error
			if retryable {
				logMismatch = a.Log().Warn
			}
			logMismatch().
				Str("asset", mismatch.assetStr).
				Strs("vault_addresses", mismatch.vaultAddrs).
				Strs("vault_pubkeys", mismatch.vaultPubKeys).
				Strs("vault_pubkeys_eddsa", mismatch.vaultPubKeysEddsa).
				Str("vault_amount", mismatch.vaultAmount.String()).
				Str("l1_amount", mismatch.l1Amount.String()).
				Str("difference", mismatch.diff.String()).
				Str("tolerance", mismatch.tolerance.String()).
				Msg("vault and L1 amounts do not match")
		}
		if retryable {
			a.Log().Warn().
				Int("attempt", attempt).
				Int("max_attempts", solvencyRetryAttempts).
				Dur("delay", solvencyRetryDelay).
				Msg("retrying solvency check after transient scanner or finality lag")
			time.Sleep(solvencyRetryDelay)
			continue
		}
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("vault and L1 amounts do not match"),
			Finish:   true,
		}
	}

	return OpResult{
		Continue: false,
		Error:    fmt.Errorf("solvency retry loop exhausted"),
		Finish:   true,
	}
}
