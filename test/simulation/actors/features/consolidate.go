package features

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Consolidate
////////////////////////////////////////////////////////////////////////////////////////

func Consolidate(rng *rand.Rand) *Actor {
	a := NewActor("Consolidate", rng)

	a.Children[NewConsolidateActor(common.BTCAsset, 10, 0, rng)] = true
	a.Children[NewConsolidateActor(common.BCHAsset, 10, 0, rng)] = true
	// DASH consolidation can spend still-unconfirmed donate UTXOs.
	// Mocknet may not mine that parent/child package until several minutes after broadcast.
	a.Children[NewConsolidateActor(common.DASHAsset, 15, 15*time.Minute, rng)] = true
	a.Children[NewConsolidateActor(common.DOGEAsset, 10, 0, rng)] = true
	a.Children[NewConsolidateActor(common.LTCAsset, 10, 0, rng)] = true
	a.Children[NewConsolidateActor(common.ZECAsset, 10, 0, rng)] = true

	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// ConsolidateActor
////////////////////////////////////////////////////////////////////////////////////////

type ConsolidateActor struct {
	Actor

	asset            common.Asset
	account          *User
	consolidateCount int64
	donateAmount     cosmos.Uint
	scanHeight       int64
}

func NewConsolidateActor(asset common.Asset, consolidateCount int64, timeout time.Duration, rng *rand.Rand) *Actor {
	a := &ConsolidateActor{
		Actor:            *NewActor(fmt.Sprintf("Feature-Consolidate-%s", asset), rng),
		asset:            asset,
		consolidateCount: consolidateCount,
	}
	a.Timeout = timeout

	a.SetLogger(a.Log().With().Str("asset", asset.String()).Logger())

	// lock a user that has sufficient L1 balance
	a.Ops = append(a.Ops, a.acquireUser)

	// get the current block height
	a.Ops = append(a.Ops, a.getHeight)

	// donate l1 balance with the consolidate count of inbounds
	for i := int64(0); i < a.consolidateCount; i++ {
		a.Ops = append(a.Ops, a.donate)
	}

	// ensure the saver is ejected and release the account
	a.Ops = append(a.Ops, a.verifyConsolidate)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *ConsolidateActor) acquireUser(config *OpConfig) OpResult {
	// determine the asset amount
	pool, err := thornode.GetPool(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pool")
		return OpResult{
			Continue: false,
		}
	}

	// donate amount is 1% of the pool asset depth
	a.donateAmount = cosmos.NewUintFromString(pool.BalanceAsset).QuoUint64(100)

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
		if l1Acct.Coins.GetCoin(a.asset).Amount.LTE(a.donateAmount) {
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

		break
	}

	// remain pending if no user is available
	return OpResult{
		Continue: a.account != nil,
	}
}

func (a *ConsolidateActor) getHeight(config *OpConfig) OpResult {
	// get the current block height
	block, err := thornode.GetBlock(0)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get block")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().Int64("height", block.Header.Height).Msg("got block height")
	a.scanHeight = block.Header.Height

	return OpResult{
		Continue: true,
	}
}

func (a *ConsolidateActor) donate(config *OpConfig) OpResult {
	memo := fmt.Sprintf("DONATE:%s", a.asset)
	client := a.account.ChainClients[a.asset.Chain]
	txid, err := DepositL1(a.Log(), client, a.asset, memo, a.donateAmount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to deposit L1")
		return SimTxErrorResult(err)
	}

	a.Log().Info().Str("txid", txid).Msg("broadcasted donate")
	return OpResult{
		Continue: true,
	}
}

func (a *ConsolidateActor) verifyConsolidate(config *OpConfig) OpResult {
	type FilterBlockResponse struct {
		Txs []struct {
			Tx struct {
				Messages []struct {
					Type  string `json:"@type"`
					QuoTx struct {
						ObsTx struct {
							Tx struct {
								Chain string `json:"chain"`
								Memo  string `json:"memo"`
							} `json:"tx"`
						} `json:"obsTx"`
					} `json:"quoTx"`
				} `json:"messages"`
			} `json:"tx"`
		} `json:"txs"`
	}

	// scan blocks until we find the consolidate tx observation
	foundConsolidate := false
	for {
		a.Log().Info().Int64("height", a.scanHeight).Msg("scanning block for consolidate")

		url := fmt.Sprintf("%s/thorchain/block?height=%d", thornode.BaseURL(), a.scanHeight)
		var block FilterBlockResponse
		err := thornode.Get(url, &block)
		if err != nil {
			return OpResult{
				Continue: false,
			}
		}
		a.scanHeight++

		// scan for a consolidate inbound
		for _, tx := range block.Txs {
			for _, msg := range tx.Tx.Messages {
				if msg.Type == "/types.MsgObservedTxQuorum" {
					if msg.QuoTx.ObsTx.Tx.Memo == "consolidate" && msg.QuoTx.ObsTx.Tx.Chain == a.asset.Chain.String() {
						foundConsolidate = true
					}
				}
			}
		}
		if foundConsolidate {
			break
		}
	}

	if !foundConsolidate {
		a.Log().Error().Msg("failed to find consolidate tx")
		return OpResult{
			Continue: false,
		}
	}

	a.account.Release() // release the user
	return OpResult{
		Continue: true,
	}
}
