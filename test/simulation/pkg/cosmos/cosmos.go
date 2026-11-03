package cosmos

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	ctypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	atypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func ctx() context.Context {
	return context.Background()
}

////////////////////////////////////////////////////////////////////////////////////////
// Cosmos
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain    common.Chain
	grpc     *grpc.ClientConn
	txConfig client.TxConfig

	keys    *thorclient.Keys
	privKey ctypes.PrivKey
	pubKey  common.PubKey
	address common.Address
	denom   string
	chainId string
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	// extract the private key
	privKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	// derive the public key
	pk, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to get tm pub key: %w", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to get pub key: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	// dial rpc
	grpc, err := grpc.NewClient(host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("fail to dial rpc: %w", err)
	}

	// setup tx config
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	interfaceRegistry.RegisterImplementations((*sdk.Msg)(nil), &btypes.MsgSend{})
	marshaler := codec.NewProtoCodec(interfaceRegistry)
	signTypes := []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT}
	txConfig := tx.NewTxConfig(marshaler, signTypes)

	var denom, chainId string

	switch chain {
	case common.GAIAChain:
		denom = "uatom"
		chainId = "localgaia"
	case common.NOBLEChain:
		denom = "uusdc"
		chainId = "localnoble"
	default:
		return nil, fmt.Errorf("unsupported chain: %s", chain)
	}

	return &Client{
		chain:    chain,
		grpc:     grpc,
		txConfig: txConfig,
		keys:     keys,
		privKey:  privKey,
		pubKey:   pubKey,
		address:  address,
		denom:    denom,
		chainId:  chainId,
	}, nil
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	address := c.address
	if pk != nil {
		var err error
		address, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	// get balances
	balanceReq := &btypes.QueryAllBalancesRequest{Address: address.String()}
	balances, err := btypes.NewQueryClient(c.grpc).AllBalances(ctx(), balanceReq)
	if err != nil {
		return nil, fmt.Errorf("fail to get account balance: %w", err)
	}

	// only atom is supported
	nativeCoins := make([]common.Coin, 0)
	for _, coin := range balances.Balances {
		if coin.Denom == c.denom {
			amount := coin.Amount.Mul(sdkmath.NewInt(100)) // 1e6 -> 1e8
			amountUint := sdkmath.NewUintFromBigInt(amount.BigInt())
			nativeCoins = append(nativeCoins, common.NewCoin(c.chain.GetGasAsset(), amountUint))
		}
	}

	// get account sequence
	accountReq := &atypes.QueryAccountRequest{Address: address.String()}
	account, err := atypes.NewQueryClient(c.grpc).Account(ctx(), accountReq)
	if err != nil {
		if isCosmosAccountNotFoundError(err) {
			return &common.Account{
				Sequence:      0,
				AccountNumber: 0,
				Coins:         nativeCoins,
			}, nil
		}
		return nil, fmt.Errorf("fail to get account: %w", err)
	}

	// decode account response
	ba := new(atypes.BaseAccount)
	err = ba.Unmarshal(account.GetAccount().Value)
	if err != nil {
		return nil, fmt.Errorf("fail to unmarshal account: %w", err)
	}

	return &common.Account{
		Sequence:      int64(ba.Sequence),
		AccountNumber: int64(ba.AccountNumber),
		Coins:         nativeCoins,
	}, nil
}

func (c *Client) SignContractTx(SimContractTx) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func isCosmosAccountNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	if status.Code(err) == codes.NotFound {
		return true
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "account") &&
		strings.Contains(errStr, "not found")
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// get account
	account, err := c.GetAccount(nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get account: %w", err)
	}

	// create message
	amount := tx.Coin.Amount.Quo(sdkmath.NewUint(100)) // 1e8 -> 1e6
	coins := []sdk.Coin{sdk.NewCoin(c.denom, sdkmath.NewIntFromBigInt(amount.BigInt()))}
	msg := &btypes.MsgSend{
		FromAddress: c.address.String(),
		ToAddress:   tx.ToAddress.String(),
		Amount:      coins,
	}

	// build transaction
	txBuilder := c.txConfig.NewTxBuilder()
	err = txBuilder.SetMsgs(msg)
	if err != nil {
		return nil, fmt.Errorf("fail to set messages: %w", err)
	}
	txBuilder.SetMemo(tx.Memo)
	txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewCoin(c.denom, sdkmath.NewInt(2000))))
	txBuilder.SetGasLimit(150_000)

	// configure signing
	sigData := &signingtypes.SingleSignatureData{
		SignMode: signingtypes.SignMode_SIGN_MODE_DIRECT,
	}
	cpk, err := c.pubKey.Decode()
	if err != nil {
		return nil, fmt.Errorf("fail to get cosmos pubkey: %w", err)
	}
	sig := signingtypes.SignatureV2{
		PubKey:   cpk,
		Data:     sigData,
		Sequence: uint64(account.Sequence),
	}

	// set signature
	err = txBuilder.SetSignatures(sig)
	if err != nil {
		return nil, fmt.Errorf("unable to initial SetSignatures on txBuilder: %w", err)
	}

	// sign transaction
	modeHandler := c.txConfig.SignModeHandler()
	signingData := authsigning.SignerData{
		ChainID:       c.chainId,
		AccountNumber: uint64(account.AccountNumber),
		Sequence:      uint64(account.Sequence),
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), modeHandler, signingtypes.SignMode_SIGN_MODE_DIRECT, signingData, txBuilder.GetTx(),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to get sign bytes: %w", err)
	}
	sigData.Signature, err = c.privKey.Sign(signBytes)
	if err != nil {
		return nil, fmt.Errorf("fail to sign: %w", err)
	}

	// verify signature
	if !cpk.VerifySignature(signBytes, sigData.Signature) {
		return nil, fmt.Errorf("fail to verify signature")
	}

	// set signatures on tx
	err = txBuilder.SetSignatures(sig)
	if err != nil {
		return nil, fmt.Errorf("fail to set signatures: %w", err)
	}

	// encode tx
	txBytes, err := c.txConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("fail to encode tx: %w", err)
	}

	return txBytes, nil
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	broadcastReq := &txtypes.BroadcastTxRequest{
		TxBytes: signed,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	}
	txService := txtypes.NewServiceClient(c.grpc)
	broadcast, err := txService.BroadcastTx(ctx(), broadcastReq)
	if err != nil {
		return "", fmt.Errorf("fail to broadcast tx: %w", err)
	}
	res := broadcast.TxResponse
	if res.Code != 0 {
		return "", fmt.Errorf("fail to broadcast tx: code %d - %s", res.Code, res.Logs.String())
	}

	// wait for block inclusion
	txReq := &txtypes.GetTxRequest{Hash: broadcast.TxResponse.TxHash}
	for {
		time.Sleep(500 * time.Millisecond)
		var response *txtypes.GetTxResponse
		response, err = txService.GetTx(ctx(), txReq)
		if err == nil && response.TxResponse != nil {
			break
		}
	}

	return broadcast.TxResponse.TxHash, nil
}
