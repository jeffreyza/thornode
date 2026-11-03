package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	tmhttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	bech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32" // nolint SA1019 deprecated
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	thored25519 "gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	eddsaKey "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

////////////////////////////////////////////////////////////////////////////////////////
// Cosmos
////////////////////////////////////////////////////////////////////////////////////////

var (
	encodingConfig params.EncodingConfig
	keyRing        keyring.Keyring
)

func init() {
	// initialize the bech32 prefix for mocknet
	config := cosmos.GetConfig()
	config.SetBech32PrefixForAccount("tthor", "tthorpub")
	config.SetBech32PrefixForValidator("tthorv", "tthorvpub")
	config.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
	config.Seal()

	// initialize the codec
	encodingConfig = app.MakeEncodingConfig()
	keyRing = keyring.NewInMemory(encodingConfig.Codec, func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{
			hd.Secp256k1,
			thored25519.Ed25519,
		}
	})

	// Having set the prefixes, derive the module addresses.
	ModuleAddrTransfer = authtypes.NewModuleAddress("transfer").String() // "tthor1yl6hdjhmkf37639730gffanpzndzdpmhv07zme"
	// "transfer" is special, as http://localhost:1317/auth/accounts/tthor1yl6hdjhmkf37639730gffanpzndzdpmhv07zme
	// gets the name from the address, but no address from name from http://localhost:1317/thorchain/balance/module/transfer
	ModuleAddrThorchain = authtypes.NewModuleAddress("thorchain").String()                    // "tthor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38ulmsrp"
	ModuleAddrAsgard = authtypes.NewModuleAddress("asgard").String()                          // "tthor1g98cy3n9mmjrpn0sxmn63lztelera37nrytwp2"
	ModuleAddrBond = authtypes.NewModuleAddress("bond").String()                              // "tthor17gw75axcnr8747pkanye45pnrwk7p9c3uhzgff"
	ModuleAddrReserve = authtypes.NewModuleAddress("reserve").String()                        // "tthor1dheycdevq39qlkxs2a6wuuzyn4aqxhve3hhmlw"
	ModuleAddrFeeCollector = authtypes.NewModuleAddress("fee_collector").String()             // "tthor17xpfvakm2amg962yls6f84z3kell8c5ljftt88"
	ModuleAddrLending = authtypes.NewModuleAddress("lending").String()                        // "tthor1x0kgm82cnj0vtmzdvz4avk3e7sj427t0al8wky"
	ModuleAddrAffiliateCollector = authtypes.NewModuleAddress("affiliate_collector").String() // "tthor1dl7un46w7l7f3ewrnrm6nq58nerjtp0d82uzjg"
	ModuleAddrTreasury = authtypes.NewModuleAddress("treasury").String()                      // "tthor1vmafl8f3s6uuzwnxkqz0eza47v6ecn0ttstnny"
	ModuleAddrRUNEPool = authtypes.NewModuleAddress("rune_pool").String()                     // "tthor1rzqfv62dzu585607s5awqtgnvvwz5rzhfuaw80"
	ModuleAddrClaiming = authtypes.NewModuleAddress("tcy_claim").String()                     // "tthor1ss8rrf3twa20kf9frdyru05dmu2kg9llwwcgag"
	ModuleAddrTCYStake = authtypes.NewModuleAddress("tcy_stake").String()                     // "tthor128a8hqnkaxyqv7qwajpggmfyudh64jl3uxmuaf"
	ModuleAddrPOLReserve = authtypes.NewModuleAddress("pol_reserve").String()                 // derived at runtime
	ModuleAddrStableReserve = authtypes.NewModuleAddress("stable_reserve").String()           // derived at runtime
}

func clientContextAndFactory(routine int) (client.Context, tx.Factory) {
	// create new rpc client
	node := fmt.Sprintf("http://localhost:%d", 26657+routine)
	rpcClient, err := tmhttp.NewWithTimeout(node, "/websocket", 5)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create tendermint client")
	}

	// create cosmos-sdk client context
	clientCtx := client.Context{
		Client:            rpcClient,
		ChainID:           "thorchain",
		Codec:             encodingConfig.Codec,
		InterfaceRegistry: encodingConfig.InterfaceRegistry,
		Keyring:           keyRing,
		BroadcastMode:     flags.BroadcastSync,
		SkipConfirm:       true,
		TxConfig:          encodingConfig.TxConfig,
		AccountRetriever:  authtypes.AccountRetriever{},
		NodeURI:           node,
		LegacyAmino:       encodingConfig.Amino,
	}

	// create tx factory
	txFactory := tx.Factory{}
	txFactory = txFactory.WithKeybase(clientCtx.Keyring)
	txFactory = txFactory.WithTxConfig(clientCtx.TxConfig)
	txFactory = txFactory.WithAccountRetriever(clientCtx.AccountRetriever)
	txFactory = txFactory.WithChainID(clientCtx.ChainID)
	txFactory = txFactory.WithGas(0)
	txFactory = txFactory.WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)

	return clientCtx, txFactory
}

////////////////////////////////////////////////////////////////////////////////////////
// Logging
////////////////////////////////////////////////////////////////////////////////////////

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	log.Logger = log.With().Caller().Logger()

	// set to info level if DEBUG is not set (debug is the default level)
	if os.Getenv("DEBUG") == "" {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Colors
////////////////////////////////////////////////////////////////////////////////////////

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorYellow = "\033[33m"
	ColorGreen  = "\033[32m"
	ColorPurple = "\033[35m"

	// save for later
	// ColorBlue   = "\033[34m"
	// ColorCyan   = "\033[36m"
	// ColorGray   = "\033[37m"
	// ColorWhite  = "\033[97m"
)

////////////////////////////////////////////////////////////////////////////////////////
// HTTP
////////////////////////////////////////////////////////////////////////////////////////

var httpClient = &http.Client{
	Transport: &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 5 * time.Second * getTimeFactor(),
		}).Dial,
	},
	Timeout: 5 * time.Second * getTimeFactor(),
}

////////////////////////////////////////////////////////////////////////////////////////
// Thorchain Module Addresses
////////////////////////////////////////////////////////////////////////////////////////

var (
	// Set these in `init` after the address prefix has been set.
	ModuleAddrTransfer           string
	ModuleAddrThorchain          string
	ModuleAddrAsgard             string
	ModuleAddrBond               string
	ModuleAddrReserve            string
	ModuleAddrFeeCollector       string
	ModuleAddrLending            string
	ModuleAddrAffiliateCollector string
	ModuleAddrTreasury           string
	ModuleAddrRUNEPool           string
	ModuleAddrClaiming           string
	ModuleAddrTCYStake           string
	ModuleAddrPOLReserve         string
	ModuleAddrStableReserve      string
)

////////////////////////////////////////////////////////////////////////////////////////
// Invariants
////////////////////////////////////////////////////////////////////////////////////////

var invariants []string

func init() {
	k := keeperv1.KVStore{}
	for _, ir := range k.InvariantRoutes() {
		invariants = append(invariants, ir.Route)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Keys
////////////////////////////////////////////////////////////////////////////////////////

var (
	addressToName      = map[string]string{} // thor...->dog, 0x...->dog
	templateAddress    = map[string]string{} // addr_thor_dog->thor..., addr_eth_dog->0x...
	templatePubKey     = map[string]string{} // pubkey_dog->thorpub...
	templateConsPubKey = map[string]string{} // cons_pubkey_dog->thorcpub...

	birdMnemonic   = strings.Repeat("bird ", 23) + "asthma"
	catMnemonic    = strings.Repeat("cat ", 23) + "crawl"
	deerMnemonic   = strings.Repeat("deer ", 23) + "diesel"
	dogMnemonic    = strings.Repeat("dog ", 23) + "fossil"
	duckMnemonic   = strings.Repeat("duck ", 23) + "face"
	fishMnemonic   = strings.Repeat("fish ", 23) + "fade"
	foxMnemonic    = strings.Repeat("fox ", 23) + "filter"
	frogMnemonic   = strings.Repeat("frog ", 23) + "flat"
	goatMnemonic   = strings.Repeat("goat ", 23) + "install"
	hawkMnemonic   = strings.Repeat("hawk ", 23) + "juice"
	lionMnemonic   = strings.Repeat("lion ", 23) + "misery"
	mouseMnemonic  = strings.Repeat("mouse ", 23) + "option"
	muleMnemonic   = strings.Repeat("mule ", 23) + "major"
	pigMnemonic    = strings.Repeat("pig ", 23) + "quick"
	rabbitMnemonic = strings.Repeat("rabbit ", 23) + "rent"
	wolfMnemonic   = strings.Repeat("wolf ", 23) + "victory"

	// mnemonics contains the set of all mnemonics for accounts used in tests
	mnemonics = [...]string{
		dogMnemonic,
		catMnemonic,
		foxMnemonic,
		pigMnemonic,
		birdMnemonic,
		deerMnemonic,
		duckMnemonic,
		fishMnemonic,
		frogMnemonic,
		goatMnemonic,
		hawkMnemonic,
		lionMnemonic,
		mouseMnemonic,
		muleMnemonic,
		rabbitMnemonic,
		wolfMnemonic,
	}

	// mnemonicByName maps an account name (e.g. "dog") to its mnemonic, derived
	// from the first word of each mnemonic above.
	mnemonicByName = func() map[string]string {
		m := make(map[string]string, len(mnemonics))
		for _, mn := range mnemonics {
			m[strings.Split(mn, " ")[0]] = mn
		}
		return m
	}()
)

// nodeSecpPrivKey derives the secp256k1 private key for a named account (e.g.
// "dog"), matching the derivation used to register keyring accounts in init().
// Used by tx operations that must sign payloads (such as price-feed quorum
// attestations) with a node's underlying key rather than the tx envelope key.
func nodeSecpPrivKey(name string) (cryptotypes.PrivKey, error) {
	mn, ok := mnemonicByName[name]
	if !ok {
		return nil, fmt.Errorf("unknown account name: %s", name)
	}
	derivedPriv, err := hd.Secp256k1.Derive()(mn, "", cmd.THORChainHDPath)
	if err != nil {
		return nil, err
	}
	return hd.Secp256k1.Generate()(derivedPriv), nil
}

func init() {
	// register functions for all mnemonic-chain addresses
	for _, m := range mnemonics {
		name := strings.Split(m, " ")[0]

		// create pubkey for mnemonic
		derivedPriv, err := hd.Secp256k1.Derive()(m, "", cmd.THORChainHDPath)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to derive private key")
		}
		privKey := hd.Secp256k1.Generate()(derivedPriv)
		ecdsaPubKey, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey.PubKey())
		if err != nil {
			log.Fatal().Err(err).Msg("failed to bech32ify ecdsa pubkey")
		}

		_, err = keyRing.NewAccount(thored25519.SignerNameEDDSA(name), m, "password", thored25519.HDPath, thored25519.Ed25519)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to add ed25519 account to keyring")
		}

		r, err := keyRing.Key(thored25519.SignerNameEDDSA(name))
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get ed25519 key from keyring")
		}

		registry := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(registry)
		cdc := codec.NewProtoCodec(registry)

		pubKey := new(cosmoscryptoed25519.PubKey)
		if err = cdc.UnpackAny(r.PubKey, &pubKey); err != nil {
			log.Fatal().Err(err).Msg("failed to unpack ed25519 pubkey")
		}
		// nolint
		ed25519PubKey, err := bech32.MarshalPubKey(bech32.AccPK, pubKey)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to marshal ed25519 pubkey to bech32")
		}

		// create consensus pubkey for ed25519
		ed25519PrivKey := eddsaKey.GenPrivKeyFromSecret([]byte(m))
		edd2519ConsPubKey, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, ed25519PrivKey.PubKey())
		if err != nil {
			log.Fatal().Err(err).Msg("failed to bech32ify EdDSA cons pubkey")
		}

		// add key to keyring
		_, err = keyRing.NewAccount(name, m, "", cmd.THORChainHDPath, hd.Secp256k1)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to add account to keyring")
		}

		for _, chain := range common.AllChains {
			// register template address for all chains
			var addr common.Address
			if chain.GetSigningAlgo() == common.SigningAlgoSecp256k1 {
				addr, err = common.PubKey(ecdsaPubKey).GetAddress(chain)
			} else if chain.GetSigningAlgo() == common.SigningAlgoEd25519 {
				addr, err = common.PubKey(ed25519PubKey).GetAddress(chain)
			}
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get address")
			}

			token := strings.ToLower(
				fmt.Sprintf("addr_%s_%s", chain.String(), name),
			)

			addresses := map[string]string{
				token: addr.String(),
			}

			// add special addresses
			if chain == common.ZECChain {
				var texAddr common.Address
				texAddr, err = addr.ToTexAddress()
				if err != nil {
					log.Warn().Err(err).
						Str("address", addr.String()).
						Msg("failed to convert to tex address")
				}

				addresses[token+"_tex"] = texAddr.String()
			}

			for k, v := range addresses {
				// register key to address (eg. addr_btc_dog)
				templateAddress[k] = v
				// register address to name
				addressToName[v] = name
			}

			// register pubkey for thorchain
			if chain == common.THORChain {
				templatePubKey[fmt.Sprintf("pubkey_%s", name)] = ecdsaPubKey
				templateConsPubKey[fmt.Sprintf("cons_pubkey_%s", name)] = edd2519ConsPubKey
				templatePubKey[fmt.Sprintf("pubkey_%s_eddsa", name)] = ed25519PubKey
			}
		}
	}
}
