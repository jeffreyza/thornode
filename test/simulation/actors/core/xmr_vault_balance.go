package core

import (
	"fmt"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	simmonero "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type xmrVaultBalanceSnapshot struct {
	inbound   common.Address
	pubKey    common.PubKey
	total     sdkmath.Uint
	spendable sdkmath.Uint
}

func (a *SwapActor) ensureXMRVaultSpendable(config *OpConfig, required sdkmath.Uint) OpResult {
	if !a.to.Chain.Equals(common.XMRChain) {
		return OpResult{Continue: true}
	}

	snapshot, err := getActiveXMRVaultBalance(config, a.user)
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for active XMR vault balance")
		return OpResult{Continue: false}
	}

	if required.IsZero() {
		required = sdkmath.NewUint(1)
	}
	if snapshot.spendable.LT(required) {
		a.Log().Info().
			Stringer("required", required).
			Stringer("spendable", snapshot.spendable).
			Stringer("total", snapshot.total).
			Stringer("vault_pubkey", snapshot.pubKey).
			Stringer("inbound_address", snapshot.inbound).
			Msg("waiting for spendable XMR in active vault")
		return OpResult{Continue: false}
	}

	return OpResult{Continue: true}
}

func getActiveXMRVaultBalance(config *OpConfig, preferredUser *User) (xmrVaultBalanceSnapshot, error) {
	client, err := getXMRVaultBalanceClient(config, preferredUser)
	if err != nil {
		return xmrVaultBalanceSnapshot{}, err
	}

	inboundAddress, _, err := thornode.GetInboundAddress(common.XMRChain)
	if err != nil {
		return xmrVaultBalanceSnapshot{}, fmt.Errorf("get active XMR inbound address: %w", err)
	}
	pubKey, err := thornode.GetInboundAddressPubKey(common.XMRChain)
	if err != nil {
		return xmrVaultBalanceSnapshot{}, fmt.Errorf("get active XMR inbound pubkey: %w", err)
	}

	totalBalance, spendableBalance, err := client.GetVaultBalance(pubKey)
	if err != nil {
		return xmrVaultBalanceSnapshot{}, fmt.Errorf("get XMR vault balance for %s: %w", pubKey, err)
	}

	return xmrVaultBalanceSnapshot{
		inbound:   inboundAddress,
		pubKey:    pubKey,
		total:     totalBalance,
		spendable: spendableBalance,
	}, nil
}

func getXMRVaultBalanceClient(config *OpConfig, preferredUser *User) (*simmonero.Client, error) {
	tryUser := func(user *User) (*simmonero.Client, bool) {
		if user == nil {
			return nil, false
		}
		client, ok := user.ChainClients[common.XMRChain]
		if !ok {
			return nil, false
		}
		xmrClient, ok := client.(*simmonero.Client)
		if !ok {
			return nil, false
		}
		return xmrClient, true
	}

	if client, ok := tryUser(preferredUser); ok {
		return client, nil
	}
	if config != nil {
		for _, user := range config.Users {
			if user == preferredUser {
				continue
			}
			if client, ok := tryUser(user); ok {
				return client, nil
			}
		}
		for _, user := range config.NodeUsers {
			if user == preferredUser {
				continue
			}
			if client, ok := tryUser(user); ok {
				return client, nil
			}
		}
	}

	return nil, fmt.Errorf("no simulation Monero client available")
}
