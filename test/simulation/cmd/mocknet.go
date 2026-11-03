package main

import (
	"errors"
	"fmt"
	"math/big"
	rand "math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog/log"

	thorapp "gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Chain RPCs
////////////////////////////////////////////////////////////////////////////////////////

var chainRPCs = map[common.Chain]string{
	common.BTCChain:   "http://127.0.0.1:18443",
	common.LTCChain:   "http://127.0.0.1:38443",
	common.BCHChain:   "http://127.0.0.1:28443",
	common.DOGEChain:  "http://127.0.0.1:18332",
	common.ZECChain:   "http://127.0.0.1:18232",
	common.ETHChain:   "http://127.0.0.1:8545",
	common.AVAXChain:  "http://127.0.0.1:9650/ext/bc/C/rpc",
	common.GAIAChain:  "127.0.0.1:9091",
	common.NOBLEChain: "127.0.0.1:9092",
	common.BASEChain:  "http://127.0.0.1:8547",
	common.POLChain:   "http://127.0.0.1:8548",
	common.TRONChain:  "http://127.0.0.1:8090",
	common.XRPChain:   "http://127.0.0.1:5005",
	common.SOLChain:   "http://127.0.0.1:8899",
	common.XMRChain:   envOrDefault("XMR_SIM_HELPER_ENDPOINT", "http://127.0.0.1:8284"),
	common.DASHChain:  "http://127.0.0.1:19898",
}

////////////////////////////////////////////////////////////////////////////////////////
// Mocknet Mnemonics
////////////////////////////////////////////////////////////////////////////////////////

var (
	mocknetMasterMnemonic = strings.Repeat("master ", 23) + "notice"

	mocknetValidatorMnemonics = [...]string{
		strings.Repeat("dog ", 23) + "fossil",
		strings.Repeat("cat ", 23) + "crawl",
		strings.Repeat("fox ", 23) + "filter",
		strings.Repeat("pig ", 23) + "quick",
		strings.Repeat("frog ", 23) + "blossom",
	}

	mocknetUserMnemonics = [...]string{
		strings.Repeat("bird ", 23) + "asthma",
		strings.Repeat("deer ", 23) + "diesel",
		strings.Repeat("duck ", 23) + "face",
		strings.Repeat("fish ", 23) + "fade",
		strings.Repeat("frog ", 23) + "flat",
		strings.Repeat("goat ", 23) + "install",
		strings.Repeat("hawk ", 23) + "juice",
		strings.Repeat("lion ", 23) + "misery",
		strings.Repeat("mouse ", 23) + "option",
		strings.Repeat("mule ", 23) + "major",
		strings.Repeat("rabbit ", 23) + "rent",
		strings.Repeat("wolf ", 23) + "victory",
	}
)

const (
	masterAccountRetryDelay           = 2 * time.Second
	masterAccountRetryAttempts        = 30
	masterAccountFundingRetryAttempts = int((10 * time.Minute) / masterAccountRetryDelay)
	mocknetInitConcurrency            = 2
	// Covers the InstantSend actor's 58 DASH retry budget with room for earlier stages.
	dashUserSeedAmount = 100 * common.One
)

// getHeightProvider lets chains with non-replayable mock histories seed
// network fees at their current chain height instead of the legacy height 1.
type getHeightProvider interface {
	GetHeight() (int64, error)
}

// getHeight preserves the old height-1 behavior for most chains, while
// allowing XRP to avoid writing a stale last-chain-height that late Bifrosts
// cannot scan from against the mock XRPL daemon.
func getHeight(chain common.Chain, clients map[common.Chain]LiteChainClient) (int64, error) {
	client, ok := clients[chain]
	if !ok {
		return 1, nil
	}

	provider, ok := client.(getHeightProvider)
	if !ok {
		return 1, nil
	}

	height, err := provider.GetHeight()
	if err != nil {
		return 0, err
	}
	if height <= 0 {
		return 0, fmt.Errorf("%s network fee height must be positive, got %d", chain, height)
	}

	return height, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Init
////////////////////////////////////////////////////////////////////////////////////////

func InitConfig(parallelism int, seed bool, rng *rand.Rand) *OpConfig {
	userCount := parallelism
	if userCount < 2 {
		log.Info().
			Int("parallelism", parallelism).
			Int("user_count", 2).
			Msg("allocating spare simulation user for background actors")
		userCount = 2
	}
	if userCount > len(mocknetUserMnemonics) {
		log.Error().
			Int("parallelism", userCount).
			Int("accounts", len(mocknetUserMnemonics)).
			Msg("parallelism limited by available user accounts")
		userCount = len(mocknetUserMnemonics)
	}

	c := &OpConfig{
		NodeUsers: make([]*User, len(mocknetValidatorMnemonics)),
	}
	mu := &sync.Mutex{}
	wg := &sync.WaitGroup{}
	// Creating a simulation user instantiates every chain client for that mnemonic.
	// Keep the startup fanout low so cluster runs do not spike Docker memory and
	// lose the sim harness to exit 137 before seed/bootstrap even begins.
	sem := make(chan struct{}, mocknetInitConcurrency)

	// since we reuse the bifrost thorclient, load endpoints into config package
	os.Setenv("BIFROST_THORCHAIN_CHAIN_HOST", "127.0.0.1:1317")
	os.Setenv("BIFROST_THORCHAIN_CHAIN_RPC", "127.0.0.1:26657")
	os.Setenv("BIFROST_THORCHAIN_CHAIN_EBIFROST", "127.0.0.1:50051")
	config.Init()

	// Build the encoding config once, up front, before the concurrent NewUser and
	// funding goroutines below. MakeEncodingConfig instantiates a temp app that
	// registers global amino/crypto codecs (not concurrency-safe); doing it here
	// single-threaded prevents racing that one-time registration against concurrent
	// key decryption (PrivKeyFromBytes) and tx broadcasts. Subsequent calls are cached.
	thorapp.MakeEncodingConfig()

	// validators
	for i, mnemonic := range mocknetValidatorMnemonics {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, mnemonic string) {
			a := NewUser(mnemonic, liteClientConstructors)
			mu.Lock()
			c.NodeUsers[i] = a
			mu.Unlock()

			defer func() {
				<-sem
				wg.Done()
			}()

			// send gaia network fee observation if this is a seed run
			if !seed {
				return
			}

			// only the first mnemonic is an active node at init
			if i != 0 {
				return
			}

			// default network fees on chains needing a window of blocks before bifrost sends.
			// Cosmos-family chains currently use a 200k gas limit with a 0.001 min gas
			// price in mocknet, so 200 uatom/uusdc is the minimum accepted fee.
			defaultFees := []struct {
				chain common.Chain
				size  uint64
				rate  uint64
			}{
				{common.GAIAChain, 1, 20_000},
				{common.NOBLEChain, 1, 20_000},
				{common.XRPChain, 1, 10},
				{common.AVAXChain, 80000, 1500},
				{common.BASEChain, 80000, 300000},
				{common.POLChain, 80000, 1500},
				{common.ETHChain, 80000, 300},
				{common.SOLChain, 1, 5000},
				{common.BTCChain, 1000, 4},
				{common.ZECChain, 1, 50_000},
				{common.LTCChain, 250, 103},
				{common.BCHChain, 1500, 7},
				{common.DOGEChain, 1000, 1500000},
				{common.TRONChain, 1, 1_000_000},
				{common.DASHChain, 2000, 10},
			}
			for _, fee := range defaultFees {
				log.Info().Msgf("posting %s network fee", fee.chain)
				for {
					height, err := getHeight(fee.chain, a.ChainClients)
					if err != nil {
						log.Error().Err(err).Stringer("chain", fee.chain).Msg("failed to get network fee height")
						time.Sleep(2 * time.Second)
						continue
					}

					_, err = a.Thorchain.PostNetworkFee(height, fee.chain, fee.size, fee.rate)
					if err == nil {
						break
					}
					log.Error().Err(err).Int64("height", height).Msg("failed to post network fee")
					time.Sleep(2 * time.Second)
				}
			}
		}(i, mnemonic)
	}

	// users
	for _, mnemonic := range mocknetUserMnemonics[:userCount] {
		wg.Add(1)
		sem <- struct{}{}
		go func(mnemonic string) {
			a := NewUser(mnemonic, liteClientConstructors)
			mu.Lock()
			c.Users = append(c.Users, a)
			mu.Unlock()
			<-sem
			wg.Done()
		}(mnemonic)
	}

	// wait for all users to be created
	wg.Wait()
	// sort users deterministically
	mu.Lock()
	sort.Slice(c.Users, func(ux_idx, uy_idx int) bool {
		return c.Users[ux_idx].Name() < c.Users[uy_idx].Name()
	})
	rng.Shuffle(len(c.Users), func(ux_idx, uy_idx int) {
		ux := c.Users[ux_idx]
		c.Users[ux_idx] = c.Users[uy_idx]
		c.Users[uy_idx] = ux
	})
	mu.Unlock()

	// fund all user accounts from master
	master := NewUser(mocknetMasterMnemonic, liteClientConstructors)

	// sort chains deterministically
	chains := make([]common.Chain, 0, len(liteClientConstructors))
	for chain := range liteClientConstructors {
		chains = append(chains, chain)
	}
	sort.Slice(chains, func(cx_idx, cy_idx int) bool {
		return chains[cx_idx] < chains[cy_idx]
	})
	rng.Shuffle(len(chains), func(cx_idx, cy_idx int) {
		cx := chains[cx_idx]
		chains[cx_idx] = chains[cy_idx]
		chains[cy_idx] = cx
	})

	// log all configured tokens, their decimals, and master balance
	for _, chain := range chains {
		account, err := waitForMasterAccount(chain, master.ChainClients[chain])
		if err != nil {
			log.Fatal().Stringer("chain", chain).Err(err).Msg("failed to get master account")
		}
		for _, coin := range account.Coins {
			ctxLog := log.Info().
				Stringer("chain", chain).
				Stringer("asset", coin.Asset).
				Stringer("address", master.Address(chain)).
				Str("amount", coin.Amount.String())

			// on evm chains, also log decimals for debugging
			if chain.IsEVM() {
				if coin.Asset.IsGasAsset() {
					ctxLog = ctxLog.Int64("decimals", chain.GetGasAssetDecimal())
				} else {
					token, ok := evm.Tokens(chain)[coin.Asset]
					if !ok {
						log.Fatal().
							Stringer("chain", chain).
							Stringer("asset", coin.Asset).
							Msg("missing configured token metadata")
					}

					evmClient := master.ChainClients[chain].(*evm.Client)
					tokenDecimals, err := evmClient.GetTokenDecimals(token.Address)
					if err != nil {
						log.Fatal().Err(err).Msg("failed to get token decimals")
					}

					// sanity check our configured token decimals
					if tokenDecimals != token.Decimals {
						log.Fatal().
							Int("actual", tokenDecimals).
							Int("configured", token.Decimals).
							Err(err).
							Msg("token decimals mismatch")
					}

					ctxLog = ctxLog.Int("decimals", tokenDecimals)
				}
			}

			// log balance
			ctxLog.Msg("master account balance")
		}
	}

	// return if not seeding accounts
	if !seed {
		return c
	}
	log.Info().Msg("initializing mocknet simulation user accounts")

	// fund all user accounts
	funded := []*User{}
	for _, user := range c.Users {
		if fundUserThorAccount(master, user) {
			funded = append(funded, user)
		}
	}

	if _, ok := liteClientConstructors[common.DASHChain]; ok {
		requiredDash := sdkmath.NewUint(uint64(len(funded)) * dashUserSeedAmount)
		client, ok := master.ChainClients[common.DASHChain]
		if !ok {
			log.Warn().Stringer("chain", common.DASHChain).Msg("no chain client found for chain, skipping funding wait")
		} else {
			log.Info().
				Stringer("chain", common.DASHChain).
				Stringer("required", requiredDash).
				Msg("waiting for master account to be funded")
			account, err := waitForMasterAccountWithMinAmountAndSleep(
				common.DASHChain,
				client,
				requiredDash,
				masterAccountFundingRetryAttempts,
				time.Sleep,
			)
			if err != nil {
				log.Fatal().Stringer("chain", common.DASHChain).Err(err).Msg("timed out waiting for genesis funding")
			}
			log.Info().
				Stringer("chain", common.DASHChain).
				Stringer("have", account.Coins.GetCoin(common.DASHChain.GetGasAsset()).Amount).
				Stringer("need", requiredDash).
				Msg("master account funded")
		}
	}

	// fund user accounts with one goroutine per chain
	wg = &sync.WaitGroup{}
	for _, chain := range acommon.SimChains {
		// determine the amount to seed
		var chainSeedAmount sdkmath.Uint
		// determine the amount of txs for seeding, to create multiple utxos
		chainSeedTxs := 1
		switch chain {
		case common.BTCChain, common.LTCChain, common.BCHChain:
			chainSeedAmount = sdkmath.NewUint(10 * common.One)
		case common.DASHChain:
			chainSeedAmount = sdkmath.NewUint(dashUserSeedAmount)
		case common.BSCChain, common.BASEChain, common.ETHChain, common.SOLChain:
			chainSeedAmount = sdkmath.NewUint(100 * common.One)
		case common.ZECChain:
			chainSeedAmount = sdkmath.NewUint(100 * common.One)
			chainSeedTxs = 20
		case common.GAIAChain, common.NOBLEChain:
			chainSeedAmount = sdkmath.NewUint(1000 * common.One)
		case common.AVAXChain, // more since local gas is high
			common.XRPChain: // more since dust threshold is 1 XRP
			chainSeedAmount = sdkmath.NewUint(10000 * common.One)
		case common.DOGEChain:
			chainSeedAmount = sdkmath.NewUint(100000 * common.One)
		case common.TRONChain:
			chainSeedAmount = sdkmath.NewUint(10000 * common.One)
		case common.POLChain:
			chainSeedAmount = sdkmath.NewUint(1_000_000 * common.One)
		case common.XMRChain:
			chainSeedAmount = sdkmath.NewUint(1000 * common.One)
		default:
			continue // all other chains currently unsupported
		}

		if chain.Equals(common.XMRChain) {
			// In its own goroutine like the other chains: it waits for the
			// faucet outputs to unlock, and inline it held back every chain
			// after XMR in SimChains (POL, ZEC) for ~30s.
			wg.Add(1)
			go func(amount sdkmath.Uint) {
				defer wg.Done()
				fundUserXMRAccounts(master, funded, amount)
			}(chainSeedAmount)
			continue
		}
		if chain.Equals(common.ZECChain) {
			wg.Add(1)
			go func(amount sdkmath.Uint, txs int) {
				defer wg.Done()
				fundUserZecAccounts(master, funded, amount, txs)
			}(chainSeedAmount.QuoUint64(uint64(chainSeedTxs)), chainSeedTxs)
			continue
		}

		for i := 0; i < chainSeedTxs; i++ {
			wg.Add(1)
			go func(chain common.Chain, amount sdkmath.Uint) {
				defer wg.Done()
				fundUserChainAccounts(master, funded, chain, amount)
			}(chain, chainSeedAmount.QuoUint64(uint64(chainSeedTxs)))
		}

	}
	wg.Wait()

	return c
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func waitForZecFunding(client LiteChainClient, target sdkmath.Uint, now func() time.Time, sleep func(time.Duration), timeout time.Duration) error {
	deadline := now().Add(timeout)
	var lastErr error
	for now().Before(deadline) {
		account, err := client.GetAccount(nil)
		lastErr = err
		if err == nil && account != nil && account.Coins.GetCoin(common.ZECAsset).Amount.GTE(target) {
			return nil
		}
		sleep(time.Second)
	}
	return fmt.Errorf("zec funding did not reach %s after %s (last RPC error: %v)", target, timeout, lastErr)
}

// nolint:typecheck
func fundUserChainAccounts(master *User, users []*User, chain common.Chain, amount sdkmath.Uint) {
	// Solana transfers carry no account nonce, so the master funds every user
	// at once; one at a time, each waited ~13s for finalized confirmation.
	if chain.Equals(common.SOLChain) {
		wg := &sync.WaitGroup{}
		for _, user := range users {
			wg.Add(1)
			go func(user *User) {
				defer wg.Done()
				fundUserChainAccount(master, user, chain, amount)
			}(user)
		}
		wg.Wait()
		return
	}
	// EVM funding sends every user's transfers back to back and then waits for
	// the receipts; one receipt at a time it took ~4s per transfer (2 per user)
	// and made POL the last chain to finish.
	if client, ok := master.ChainClients[chain].(*evm.Client); ok {
		client.DeferReceipts()
		for _, user := range users {
			fundUserChainAccount(master, user, chain, amount)
		}
		if err := client.FlushReceipts(); err != nil {
			log.Fatal().Err(err).Stringer("chain", chain).Msg("funding tx failed")
		}
		return
	}
	for _, user := range users {
		fundUserChainAccount(master, user, chain, amount)
	}
}

// zecFundingTransfers splits what a user still needs to reach txs*amount into
// transfers of at most amount each, so a fresh user ends up with txs UTXOs.
func zecFundingTransfers(current, amount sdkmath.Uint, txs int) []sdkmath.Uint {
	target := amount.MulUint64(uint64(txs))
	if amount.IsZero() || current.GTE(target) {
		return nil
	}
	var transfers []sdkmath.Uint
	for missing := target.Sub(current); !missing.IsZero(); {
		send := sdkmath.MinUint(amount, missing)
		transfers = append(transfers, send)
		missing = missing.Sub(send)
	}
	return transfers
}

// fundUserZecAccounts broadcasts every transfer for every user, then waits for
// the balances once. The ZEC client keeps the master's inputs reserved until
// each payload is accepted, so transfers in flight never share a UTXO. The
// plan is computed from the balance before any transfer, and the only wait is
// at the end, so an unmined transfer is never paid twice.
func fundUserZecAccounts(master *User, users []*User, amount sdkmath.Uint, txs int) {
	target := amount.MulUint64(uint64(txs))
	for _, user := range users {
		account, err := user.ChainClients[common.ZECChain].GetAccount(nil)
		if err != nil {
			log.Fatal().Err(err).Str("user", user.Name()).Msg("failed to get user zec account")
		}
		current := account.Coins.GetCoin(common.ZECAsset).Amount
		transfers := zecFundingTransfers(current, amount, txs)
		if len(transfers) == 0 {
			log.Info().
				Str("user", user.Name()).
				Stringer("balance", current).
				Stringer("target", target).
				Msg("zec account already funded")
			continue
		}
		for _, transfer := range transfers {
			broadcastFunding(master, user, common.ZECChain, transfer)
		}
	}
	for _, user := range users {
		if err := waitForZecFunding(user.ChainClients[common.ZECChain], target, time.Now, time.Sleep, 2*time.Minute); err != nil {
			log.Fatal().Err(err).Str("user", user.Name()).Msg("failed waiting for zec funding confirmation")
		}
	}
}

// broadcastFunding sends fundingAmount of the chain's gas asset from master to user.
func broadcastFunding(master, user *User, chain common.Chain, fundingAmount sdkmath.Uint) {
	addr, err := user.PubKey(chain).GetAddress(chain)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get address")
	}
	tx := SimTx{
		Chain:     chain,
		ToAddress: addr,
		Coin:      common.NewCoin(chain.GetGasAsset(), fundingAmount),
		Memo:      fmt.Sprintf("SIMULATION:%s", user.Name()),
	}

	txID, err := signAndBroadcastFundingTx(master.ChainClients[chain], tx, time.Sleep)
	if err != nil {
		log.Fatal().Err(err).Interface("tx", tx).Msg("failed to broadcast funding tx")
	}

	amountFloat := float64(fundingAmount.Uint64()) / float64(common.One)
	log.Info().
		Str("txid", txID).
		Str("user", user.Name()).
		Stringer("chain", chain).
		Stringer("address", addr).
		Str("amount", fmt.Sprintf("%08f", amountFloat)).
		Msg("account funded")
}

// signAndBroadcastFundingTx restores Dash funding retries while preserving the
// chain client's terminal uncertainty result after a possibly accepted broadcast.
func signAndBroadcastFundingTx(client LiteChainClient, tx SimTx, sleep func(time.Duration)) (string, error) {
	maxAttempts := 1
	if tx.Chain.Equals(common.DASHChain) {
		maxAttempts = 5
	}
	for attempt := 1; ; attempt++ {
		txID, err := SignAndBroadcastTx(client, tx)
		if err == nil {
			return txID, nil
		}
		if errors.Is(err, ErrSimTxUncertain) || txID != "" {
			return txID, err
		}
		if attempt == maxAttempts {
			return txID, err
		}
		log.Warn().Err(err).
			Stringer("chain", tx.Chain).
			Stringer("to", tx.ToAddress).
			Int("attempt", attempt).
			Msg("retrying funding transaction")
		sleep(2 * time.Second)
	}
}

// nolint:typecheck
func fundUserChainAccount(master, user *User, chain common.Chain, amount sdkmath.Uint) {
	addr, err := user.PubKey(chain).GetAddress(chain)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get address")
	}

	userAccount, err := user.ChainClients[chain].GetAccount(nil)
	if err != nil {
		log.Fatal().Err(err).Stringer("chain", chain).Msg("failed to get user account")
	}

	currentAmount := userAccount.Coins.GetCoin(chain.GetGasAsset()).Amount
	if currentAmount.GTE(amount) {
		log.Info().
			Str("user", user.Name()).
			Stringer("chain", chain).
			Stringer("address", addr).
			Stringer("balance", currentAmount).
			Stringer("target", amount).
			Msg("account already funded")
	} else {
		broadcastFunding(master, user, chain, amount.Sub(currentAmount))
	}

	// if this is an EVM chain also fund token balances
	if !chain.IsEVM() {
		return
	}

	// fund token balances
	eAddr := ecommon.HexToAddress(addr.String())
	for asset, token := range evm.Tokens(chain) {
		// Cap EVM token seeding so the master mocknet inventory, especially on AVAX,
		// can cover every simulation user without later ERC-20 transfers reverting.
		targetTokenAmount := sdkmath.NewUint(5_000 * common.One)
		if amount.LT(targetTokenAmount) {
			targetTokenAmount = amount
		}

		currentTokenAmount := userAccount.Coins.GetCoin(asset).Amount
		if currentTokenAmount.GTE(targetTokenAmount) {
			log.Info().
				Str("account", user.Name()).
				Stringer("asset", asset).
				Stringer("balance", currentTokenAmount).
				Stringer("target", targetTokenAmount).
				Msg("token balance already funded")
			continue
		}
		tokenFundingAmount := targetTokenAmount.Sub(currentTokenAmount)

		// convert funding amount to token decimals
		factor := big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil)
		tokenAmount := tokenFundingAmount.Mul(sdkmath.NewUintFromBigInt(factor))
		tokenAmount = tokenAmount.Quo(sdkmath.NewUint(common.One))

		tokenTx := SimContractTx{
			Chain:    chain,
			Contract: common.Address(token.Address),
			ABI:      evm.ERC20ABI(),
			Method:   "transfer",
			Args:     []interface{}{eAddr, tokenAmount.BigInt()},
		}
		tokenSigned, err := master.ChainClients[chain].(*evm.Client).SignContractTx(tokenTx)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to sign master token tx")
		}
		tokenTxid, err := master.ChainClients[chain].BroadcastTx(tokenSigned)
		if err != nil {
			from, _ := master.PubKey(chain).GetAddress(chain)
			log.Fatal().Err(err).
				Stringer("chain", chain).
				Stringer("from", from).
				Msg("failed to broadcast funding token tx")
		}
		amountFloat := float64(tokenFundingAmount.Uint64()) / float64(common.One)
		log.Info().
			Str("txid", tokenTxid).
			Str("account", user.Name()).
			Stringer("asset", asset).
			Stringer("address", addr).
			Str("token", token.Address).
			Str("amount", fmt.Sprintf("%08f", amountFloat)).
			Msg("token balance funded")
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func thorUserFundingPlan(coins common.Coins) (seedThor bool, seedChains bool) {
	if len(coins) > 0 {
		return false, true
	}
	return true, true
}

func fundUserThorAccount(master, user *User) bool {
	masterThorAddress, err := master.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get master thor address")
	}

	// skip seeding user if thorchain account has balance
	userThorAddress, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get user thor address")
	}
	coins, _ := thornode.GetBalances(userThorAddress)
	seedThor, seedChains := thorUserFundingPlan(coins)
	if !seedThor {
		log.Info().Str("account", user.Name()).Msg("user has rune, skipping seed")
		return seedChains
	}

	// seed thorchain account
	userThorAccAddress, err := user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get user thor address")
	}
	seedAmount := sdkmath.NewInt(1000 * common.One)
	seedAmountFloat := float64(seedAmount.Uint64()) / float64(common.One)
	tx := &ttypes.MsgSend{
		FromAddress: masterThorAddress,
		ToAddress:   userThorAccAddress,
		Amount:      sdk.NewCoins(sdk.NewCoin("rune", seedAmount)),
	}
	thorTxid, err := master.Thorchain.Broadcast(tx)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to broadcast tx")
	}
	log.Info().
		Stringer("txid", thorTxid).
		Str("account", user.Name()).
		Stringer("chain", common.THORChain).
		Stringer("address", userThorAccAddress).
		Str("amount", fmt.Sprintf("%08f", seedAmountFloat)).
		Msg("account funded")

	return seedChains
}

func fundUserXMRAccounts(master *User, users []*User, amount sdkmath.Uint) {
	client, ok := master.ChainClients[common.XMRChain].(*monero.Client)
	if !ok {
		log.Fatal().Msg("failed to get monero simulation client")
	}

	birthdayHeight, err := client.GetHeight()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get XMR simulation birthday height")
	}
	client.SetBirthdayHeight(uint64(birthdayHeight))
	for _, user := range users {
		userClient, ok := user.ChainClients[common.XMRChain].(*monero.Client)
		if !ok {
			log.Fatal().Str("user", user.Name()).Msg("failed to get user monero simulation client")
		}
		userClient.SetBirthdayHeight(uint64(birthdayHeight))
	}
	log.Info().
		Uint64("birthday_height", uint64(birthdayHeight)).
		Msg("set XMR simulation account birthday height")

	for _, user := range users {
		addr, err := user.PubKey(common.XMRChain).GetAddress(common.XMRChain)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get XMR address")
		}

		txid, err := client.Faucet(addr, amount)
		if err != nil {
			log.Fatal().Err(err).Stringer("address", addr).Msg("failed to faucet XMR simulation account")
		}

		amountFloat := float64(amount.Uint64()) / float64(common.One)
		log.Info().
			Str("txid", txid).
			Str("user", user.Name()).
			Stringer("chain", common.XMRChain).
			Stringer("address", addr).
			Str("amount", fmt.Sprintf("%08f", amountFloat)).
			Msg("account funded")
	}

	for _, user := range users {
		for {
			acct, err := user.ChainClients[common.XMRChain].GetAccount(nil)
			if err == nil && acct.Coins.GetCoin(common.XMRAsset).Amount.GTE(amount) {
				break
			}

			if err != nil {
				log.Warn().Err(err).Str("user", user.Name()).Msg("waiting for XMR simulation account balance")
			} else {
				log.Info().
					Str("user", user.Name()).
					Stringer("balance", acct.Coins.GetCoin(common.XMRAsset).Amount).
					Stringer("target", amount).
					Msg("waiting for XMR funds to unlock")
			}

			time.Sleep(2 * time.Second)
		}
	}
}

func waitForMasterAccount(chain common.Chain, client LiteChainClient) (*common.Account, error) {
	return waitForMasterAccountWithMinAmountAndSleep(
		chain,
		client,
		sdkmath.ZeroUint(),
		masterAccountRetryAttempts,
		time.Sleep,
	)
}

// waitForMasterAccountWithMinAmountAndSleep polls for account readiness, retrying
// temporary startup errors and insufficient balances. Tests can supply their own sleep function.
func waitForMasterAccountWithMinAmountAndSleep(
	chain common.Chain,
	client LiteChainClient,
	minAmount sdkmath.Uint,
	maxAttempts int,
	sleep func(time.Duration),
) (*common.Account, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		account, err := client.GetAccount(nil)
		if err == nil && isMasterAccountReady(chain, account, minAmount) {
			return account, nil
		}
		if err == nil {
			lastErr = masterAccountBalanceNotReadyError(chain, account, minAmount)
			if !isRetryableMasterAccountBalanceState(chain, account, minAmount) || attempt == maxAttempts {
				return nil, lastErr
			}
		} else {
			lastErr = err
			if !isRetryableMasterAccountStartupError(chain, err) || attempt == maxAttempts {
				return nil, err
			}
		}

		log.Warn().
			Stringer("chain", chain).
			Int("attempt", attempt).
			Int("max_attempts", maxAttempts).
			Err(lastErr).
			Msg("waiting for master account readiness")
		sleep(masterAccountRetryDelay)
	}

	return nil, lastErr
}

func isMasterAccountReady(chain common.Chain, account *common.Account, minAmount sdkmath.Uint) bool {
	if account == nil {
		return false
	}

	if !minAmount.IsZero() {
		return account.Coins.GetCoin(chain.GetGasAsset()).Amount.GTE(minAmount)
	}

	return !isRetryableZeroBalanceStartupState(chain, account)
}

func requiresFundedMasterAccount(chain common.Chain) bool {
	return chain.Equals(common.XRPChain) || chain.Equals(common.SOLChain)
}

func isRetryableZeroBalanceStartupState(chain common.Chain, account *common.Account) bool {
	return isRetryableMasterAccountBalanceState(chain, account, sdkmath.ZeroUint())
}

// isRetryableMasterAccountBalanceState reports whether an existing account needs
// more funds before simulation setup can continue.
func isRetryableMasterAccountBalanceState(chain common.Chain, account *common.Account, minAmount sdkmath.Uint) bool {
	if account == nil {
		return false
	}

	amount := account.Coins.GetCoin(chain.GetGasAsset()).Amount
	if !minAmount.IsZero() {
		return amount.LT(minAmount)
	}

	return requiresFundedMasterAccount(chain) && amount.IsZero()
}

// masterAccountBalanceNotReadyError describes the balance that prevents account readiness.
func masterAccountBalanceNotReadyError(chain common.Chain, account *common.Account, minAmount sdkmath.Uint) error {
	amount := sdkmath.ZeroUint()
	if account != nil {
		amount = account.Coins.GetCoin(chain.GetGasAsset()).Amount
	}
	if !minAmount.IsZero() {
		return fmt.Errorf("%s master account balance %s is below required %s", chain, amount, minAmount)
	}

	return fmt.Errorf("%s master account balance is still zero", chain)
}

func isRetryableMasterAccountStartupError(chain common.Chain, err error) bool {
	return isRetryableXMRStartupError(chain, err) || isRetryableTransportStartupError(err)
}

func isRetryableXMRStartupError(chain common.Chain, err error) bool {
	if err == nil || !chain.Equals(common.XMRChain) {
		return false
	}

	msg := err.Error()
	return (strings.Contains(msg, "failed to fetch scannable block 1") &&
		strings.Contains(msg, "Requested block height: 1 greater than current top block height: 0")) ||
		strings.Contains(msg, "failed to query daemon height:") ||
		strings.Contains(msg, "daemon RPC get_info failed")
}

func isRetryableTransportStartupError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	for _, snippet := range []string{
		"context deadline exceeded",
		"connect: connection refused",
		"connection reset by peer",
		"EOF",
		"i/o timeout",
	} {
		if strings.Contains(msg, snippet) {
			return true
		}
	}

	return false
}
