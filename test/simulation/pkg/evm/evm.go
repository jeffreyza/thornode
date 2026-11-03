package evm

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"time"

	_ "embed"

	sdkmath "cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

const ContractGasLimit = 3000000

// chainGasOffset makes simulator-signed EVM transactions chain-distinct even
// when mock geth nodes all run with the dev chain ID 1337. The offset is tiny
// enough not to affect execution, but it changes the signed tx bytes so two
// mock chains cannot produce the same tx hash from the same account/nonce/memo.
func chainGasOffset(chain common.Chain) uint64 {
	switch chain {
	case common.ETHChain:
		return 0
	case common.BSCChain:
		return 1
	case common.BASEChain:
		return 2
	case common.POLChain:
		return 3
	case common.AVAXChain:
		return 4
	default:
		return 0
	}
}

// MocknetAggregatorAddress is the deterministic address of the simulation
// aggregator contract deployed by build/scripts/evm/evm-tool.py. The address
// is identical on every EVM mocknet chain because the same admin key and
// deploy nonce sequence is used. The matching whitelist entry lives in
// x/thorchain/aggregators/dex_mocknet.go.
const MocknetAggregatorAddress = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"

// MocknetAggregatorSuffix is the trailing fragment used by simulation memos
// to reference MocknetAggregatorAddress through FetchDexAggregator.
const MocknetAggregatorSuffix = "aad6d"

////////////////////////////////////////////////////////////////////////////////////////
// Init
////////////////////////////////////////////////////////////////////////////////////////

//go:embed abi/router.json
var routerABIJson string

//go:embed abi/erc20.json
var erc20ABIJson string

//go:embed abi/aggregator.json
var aggregatorABIJson string

var routerABI, erc20ABI, aggregatorABI abi.ABI

func init() {
	var err error
	routerABI, err = abi.JSON(strings.NewReader(routerABIJson))
	if err != nil {
		panic(fmt.Errorf("failed to parse router contract abi: %w", err))
	}

	erc20ABI, err = abi.JSON(strings.NewReader(erc20ABIJson))
	if err != nil {
		panic(fmt.Errorf("failed to parse erc20 contract abi: %w", err))
	}

	aggregatorABI, err = abi.JSON(strings.NewReader(aggregatorABIJson))
	if err != nil {
		panic(fmt.Errorf("failed to parse aggregator contract abi: %w", err))
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// ABI
////////////////////////////////////////////////////////////////////////////////////////

func RouterABI() abi.ABI {
	return routerABI
}

func ERC20ABI() abi.ABI {
	return erc20ABI
}

func AggregatorABI() abi.ABI {
	return aggregatorABI
}

////////////////////////////////////////////////////////////////////////////////////////
// Tokens
////////////////////////////////////////////////////////////////////////////////////////

// Tokens returns the list of tokens that are used in the simulation. All tokens will be
// looked up and included in the GetAccount response. This method can be replicated with
// build tag scope for testing different environments in the future.
func Tokens(chain common.Chain) map[common.Asset]tokenlist.ERC20Token {
	tokenMap := make(map[common.Asset]tokenlist.ERC20Token)

	// gather the available tokens
	tokens := []tokenlist.ERC20Token{
		{
			Address:  "0x17aB05351fC94a1a67Bf3f56DdbB941aE6c63E25",
			Symbol:   "TKN",
			Decimals: 18,
		},
	}

	// create mapping of asset to token
	for _, token := range tokens {
		tokenMap[token.Asset(chain)] = token
	}

	return tokenMap
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func ctx() context.Context {
	return context.Background()
}

////////////////////////////////////////////////////////////////////////////////////////
// Client
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain common.Chain
	rpc   *ethclient.Client

	keys    *thorclient.Keys
	privKey *ecdsa.PrivateKey
	signer  etypes.EIP155Signer
	pubKey  common.PubKey
	address common.Address

	// deferred holds transactions BroadcastTx sent without waiting for their
	// receipt while DeferReceipts is on; FlushReceipts waits for them.
	deferring bool
	deferred  []ecommon.Hash
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	// extract the private key
	privateKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	privKey, err := evm.GetPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}

	// derive the public key
	pk, err := cryptocodec.ToCmtPubKeyInterface(privateKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("failed to get tm pub key: %w", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to create pubkey: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	// dial the rpc host
	rpc, err := ethclient.Dial(host)
	if err != nil {
		return nil, fmt.Errorf("fail to dial ETH rpc host(%s): %w", host, err)
	}

	// get the chain id
	chainID, err := rpc.ChainID(ctx())
	if err != nil {
		return nil, fmt.Errorf("fail to get chain id: %w", err)
	}

	// create the signer
	signer := etypes.NewEIP155Signer(chainID)

	return &Client{
		chain:   chain,
		rpc:     rpc,
		keys:    keys,
		privKey: privKey,
		signer:  signer,
		pubKey:  pubKey,
		address: address,
	}, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// GetAccount
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	address := c.address
	if pk != nil {
		var err error
		address, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	// get nonce
	nonce, err := c.rpc.PendingNonceAt(ctx(), ecommon.HexToAddress(address.String()))
	if err != nil {
		return nil, fmt.Errorf("fail to get account nonce: %w", err)
	}

	// get balance
	balance, err := c.rpc.BalanceAt(ctx(), ecommon.HexToAddress(address.String()), nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get account balance: %w", err)
	}

	// get amount
	amount := sdkmath.NewUintFromBigInt(balance)
	amount = amount.Quo(sdkmath.NewUint(1e10)) // 1e18 -> 1e8

	// add gas asset to coins
	coins := common.Coins{
		common.NewCoin(c.chain.GetGasAsset(), amount),
	}

	// lookup any other tokens
	for asset, token := range Tokens(c.chain) {
		// get balance
		abi := ERC20ABI()
		var data []byte
		data, err = abi.Pack("balanceOf", ecommon.HexToAddress(address.String()))
		if err != nil {
			log.Error().Err(err).Msg("error packing balanceOf")
			continue
		}
		to := ecommon.HexToAddress(token.Address)
		var result []byte
		result, err = c.rpc.CallContract(ctx(), ethereum.CallMsg{
			To:   &to,
			Data: data,
		}, nil)
		if err != nil {
			log.Error().Err(err).Msg("error calling contract")
			continue
		}
		balance = new(big.Int)
		balance.SetBytes(result)

		// convert balance from decimals to 1e8
		balance.Mul(balance, big.NewInt(common.One))
		balance.Div(balance, big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil))

		// add to coins
		coins = append(coins, common.NewCoin(asset, sdkmath.NewUintFromBigInt(balance)))
	}

	// create account
	return &common.Account{
		Sequence: int64(nonce),
		Coins:    coins,
	}, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// SignTx
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// to address to evm address
	toAddress := ecommon.HexToAddress(tx.ToAddress.String())

	// create a standard transfer tx
	gasPerByte := uint64(40) // https://eips.ethereum.org/EIPS/eip-7623
	gasLimit := uint64(21000) + uint64(len(tx.Memo))*gasPerByte + chainGasOffset(tx.Chain)
	txData := &etypes.LegacyTx{
		To:    &toAddress,
		Data:  []byte(tx.Memo),
		Gas:   gasLimit,                                           // transfer + memo
		Value: tx.Coin.Amount.Mul(sdkmath.NewUint(1e10)).BigInt(), // 1e8 -> 1e18,
	}

	return c.signTx(txData)
}

// signTx is shared by the base SignTx and EVM custom SignContractTx methods.
func (c *Client) signTx(txData *etypes.LegacyTx) ([]byte, error) {
	// get nonce
	nonce, err := c.rpc.PendingNonceAt(ctx(), ecommon.HexToAddress(c.address.String()))
	if err != nil {
		return nil, fmt.Errorf("fail to get account nonce: %w", err)
	}

	// get gas price
	gasPrice, err := c.rpc.SuggestGasPrice(ctx())
	if err != nil {
		return nil, fmt.Errorf("fail to get gas price: %w", err)
	}

	// set nonce and gas price
	txData.Nonce = nonce
	txData.GasPrice = gasPrice

	// create signable tx
	signable := etypes.NewTx(txData)

	// sign the tx
	hash := c.signer.Hash(signable)
	sig, err := ecrypto.Sign(hash[:], c.privKey)
	if err != nil {
		return nil, fmt.Errorf("fail to sign tx: %w", err)
	}

	// apply the signature
	newTx, err := signable.WithSignature(c.signer, sig)
	if err != nil {
		return nil, fmt.Errorf("fail to apply signature to tx: %w", err)
	}

	// marshal and return
	enc, err := newTx.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("fail to marshal tx to json: %w", err)
	}

	return enc, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// BroadcastTx
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	tx := &etypes.Transaction{}
	if err := tx.UnmarshalJSON(signed); err != nil {
		return "", err
	}
	txid := tx.Hash().String()

	// remove 0x prefix
	txid = strings.TrimPrefix(txid, "0x")

	// send the transaction
	err := c.rpc.SendTransaction(ctx(), tx)
	if err != nil {
		return txid, err
	}

	if c.deferring {
		c.deferred = append(c.deferred, tx.Hash())
		return txid, nil
	}
	return txid, c.waitReceipt(tx.Hash(), 10)
}

// DeferReceipts makes BroadcastTx return once the node accepts a transaction,
// without waiting for its receipt, until FlushReceipts. Signing uses the
// pending nonce, so back-to-back transactions from this client still get
// consecutive nonces. The client must not be shared while deferring.
func (c *Client) DeferReceipts() {
	c.deferring = true
}

// FlushReceipts waits for every transaction sent since DeferReceipts, checks
// that each succeeded, and turns deferring off.
func (c *Client) FlushReceipts() error {
	defer func() {
		c.deferring = false
		c.deferred = nil
	}()
	for _, hash := range c.deferred {
		if err := c.waitReceipt(hash, 60); err != nil {
			return fmt.Errorf("%s: %w", strings.TrimPrefix(hash.String(), "0x"), err)
		}
	}
	return nil
}

// waitReceipt polls for the receipt of hash once a second, up to attempts times,
// and fails if the transaction reverted.
func (c *Client) waitReceipt(hash ecommon.Hash, attempts int) error {
	var receipt *etypes.Receipt
	var err error
	for i := 0; i < attempts; i++ {
		receipt, err = c.rpc.TransactionReceipt(ctx(), hash)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("fail to get transaction receipt: %w", err)
	}

	// check the status and return logs in error
	if receipt.Status != 1 {
		return fmt.Errorf("transaction failed with status %d: %v", receipt.Status, receipt.Logs)
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Custom EVM Client Methods
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) SignContractTx(tx SimContractTx) ([]byte, error) {
	// contract address to evm address
	contractAddress := ecommon.HexToAddress(tx.Contract.String())
	data, err := tx.ABI.Pack(tx.Method, tx.Args...)
	if err != nil {
		return nil, fmt.Errorf("fail to pack contract call: %w", err)
	}

	value := big.NewInt(0)
	if tx.Value != nil {
		value = new(big.Int).Set(tx.Value)
	}

	// create the tx
	txData := &etypes.LegacyTx{
		To:    &contractAddress,
		Data:  data,
		Gas:   ContractGasLimit + chainGasOffset(tx.Chain),
		Value: value,
	}

	return c.signTx(txData)
}

func (c *Client) GetTokenDecimals(address string) (int, error) {
	// build contract read call
	addr := ecommon.HexToAddress(address)
	abi := ERC20ABI()
	data, err := abi.Pack("decimals")
	if err != nil {
		return 0, fmt.Errorf("fail to pack decimals call: %w", err)
	}

	// read the contract
	result, err := c.rpc.CallContract(ctx(), ethereum.CallMsg{
		To:   &addr,
		Data: data,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("fail to call contract: %w", err)
	}

	// extract the decimals
	decimals := new(big.Int)
	decimals.SetBytes(result)
	return int(decimals.Uint64()), nil
}

// GetTokenBalance reads the ERC20 balance of the given owner address for the
// token at tokenAddress. The result is returned in token-native decimals so
// callers can compare against amounts sent through aggregator finalToken
// deliveries that bypass THORChain accounting.
func (c *Client) GetTokenBalance(tokenAddress, ownerAddress string) (*big.Int, error) {
	tokenAddr := ecommon.HexToAddress(tokenAddress)
	ownerAddr := ecommon.HexToAddress(ownerAddress)
	abi := ERC20ABI()
	data, err := abi.Pack("balanceOf", ownerAddr)
	if err != nil {
		return nil, fmt.Errorf("fail to pack balanceOf call: %w", err)
	}

	result, err := c.rpc.CallContract(ctx(), ethereum.CallMsg{
		To:   &tokenAddr,
		Data: data,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("fail to call contract: %w", err)
	}

	balance := new(big.Int)
	balance.SetBytes(result)
	return balance, nil
}

// HasContractCode returns true when the address has bytecode deployed. This is
// used by simulation actors to decide whether the aggregator they intend to
// reach is a deployed contract or only a whitelisted address.
func (c *Client) HasContractCode(address string) (bool, error) {
	addr := ecommon.HexToAddress(address)
	code, err := c.rpc.CodeAt(ctx(), addr, nil)
	if err != nil {
		return false, fmt.Errorf("fail to get code: %w", err)
	}
	return len(code) > 0, nil
}

func (c *Client) GetVaultAllowance(router, vault common.Address, asset common.Asset) (cosmos.Uint, error) {
	// build contract read call
	routerAddr := ecommon.HexToAddress(router.String())
	vaultAddr := ecommon.HexToAddress(vault.String())
	token := Tokens(asset.Chain)[asset]
	tokenAddr := ecommon.HexToAddress(token.Address)
	abi := RouterABI()
	data, err := abi.Pack("vaultAllowance", vaultAddr, tokenAddr)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to pack vaultAllowance call: %w", err)
	}

	// read the contract
	result, err := c.rpc.CallContract(ctx(), ethereum.CallMsg{
		To:   &routerAddr,
		Data: data,
	}, nil)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to call contract: %w", err)
	}

	// extract the allowance
	allowance := new(big.Int)
	allowance.SetBytes(result)

	// convert balance from decimals to 1e8
	allowance.Mul(allowance, big.NewInt(common.One))
	allowance.Div(allowance, big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil))
	return cosmos.NewUintFromBigInt(allowance), nil
}
