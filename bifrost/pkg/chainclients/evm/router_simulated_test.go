package evm

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// RouterSimSuite runs the real EVMClient against an in-process chain (simChain) with
// the router and token bytecode mocknet deploys, so eth_estimateGas executes the
// router and reverts the way a node would. The JSON-RPC mocks in client_test.go
// return a constant estimate and never revert, which is how two Router V6 (!4094)
// outbound bugs passed unit tests: the native transferOut estimate packed the real
// amount against a placeholder msg.value ("TC:eth amount mismatch"), and SignTx
// estimated an ERC20 transferOut before broadcasting the vault's approval, so the
// estimate reverted and the approval was never sent.
type RouterSimSuite struct {
	chain    *simChain
	deployer *ecdsa.PrivateKey
	client   *EVMClient
	vaultPk  common.PubKey
	vault    ecommon.Address
	router   ecommon.Address
	token    ecommon.Address
}

var _ = Suite(&RouterSimSuite{})

const (
	// router-bytecode.txt is THORChain_RouterV6, deployed by evm-tool.py on every
	// mocknet EVM chain except POL (which needs the pre-Shanghai paris build).
	simRouterBytecode = "../../../../build/scripts/evm/router-bytecode.txt"
	simERC20Bytecode  = "../../../../build/scripts/evm/token-bytecode.txt"
	simGasRateGwei    = 10
	simMaxGas         = 1_000_000 // 0.01 BNB in 1e8 units: 1M gas at 10 gwei
)

var (
	simEther     = big.NewInt(1e18)
	simRecipient = ecommon.HexToAddress("0x1111111111111111111111111111111111111111")
)

// simBridge answers the THORChain queries SignTx and BroadcastTx make. Any other call
// reaches the nil embedded interface and panics, which flags new coupling.
type simBridge struct {
	thorclient.ThorchainBridge
	mimir map[string]int64
}

func (b *simBridge) GetMimir(key string) (int64, error) {
	if v, ok := b.mimir[key]; ok {
		return v, nil
	}
	return -1, nil
}

func (b *simBridge) GetBlockHeight() (int64, error) { return 1, nil }

// simPubKeys reports the deployed router as every vault's contract.
type simPubKeys struct {
	*pubkeymanager.MockPoolAddressValidator
	router common.Address
}

func (p simPubKeys) GetContract(common.Chain, common.PubKey) common.Address { return p.router }

func (s *RouterSimSuite) SetUpTest(c *C) {
	setupMocknetThorConfig()

	// The vault is the client's own key, so SignTx signs locally instead of via TSS.
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := cKeys.NewInMemory(codec.NewProtoCodec(registry))
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
	_, err := kb.NewAccount("bob", mnemonic, "", cmd.THORChainHDPath, hd.Secp256k1)
	c.Assert(err, IsNil)
	thorKeys := thorclient.NewKeysWithKeybase(kb, "bob", "password")
	priv, err := thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	cmtPub, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	s.vaultPk, err = common.NewPubKeyFromCrypto(cmtPub)
	c.Assert(err, IsNil)
	vaultAddr, err := s.vaultPk.GetAddress(common.BSCChain)
	c.Assert(err, IsNil)
	s.vault = ecommon.HexToAddress(vaultAddr.String())

	s.deployer, err = ecrypto.GenerateKey()
	c.Assert(err, IsNil)
	hundred := new(big.Int).Mul(big.NewInt(100), simEther)
	s.chain, err = newSimChain(etypes.GenesisAlloc{
		s.vault: {Balance: hundred},
		ecrypto.PubkeyToAddress(s.deployer.PublicKey): {Balance: hundred},
	})
	c.Assert(err, IsNil)
	s.router = s.deploy(c, simRouterBytecode)
	s.token = s.deploy(c, simERC20Bytecode)

	bridge := &simBridge{mimir: map[string]int64{
		fmt.Sprintf(constants.MimirTemplateEVMAllowanceCheck, common.BSCChain): 1,
	}}
	pubKeys := simPubKeys{
		MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
		router:                   common.Address(s.router.Hex()),
	}
	s.client, err = NewEVMClient(thorKeys, config.BifrostChainConfiguration{
		ChainID: common.BSCChain, // 450ms blocks keep SignTx's post-approval wait short
		RPCHost: s.chain.URL(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.BSCChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: 10 * time.Second,
			MaxGasLimit:        100_000,
		},
	}, nil, bridge, GetMetricForTest(c), pubKeys, thorclient.NewPoolMgr(bridge))
	c.Assert(err, IsNil)
	s.client.signingReady.Store(true)
}

func (s *RouterSimSuite) TearDownTest(c *C) {
	if s.client != nil {
		s.client.ethClient.Close()
		s.client = nil
	}
	if s.chain != nil {
		s.chain.close()
		s.chain = nil
	}
}

// send signs, mines and requires success of a deployer transaction.
func (s *RouterSimSuite) send(c *C, to *ecommon.Address, data []byte) *etypes.Receipt {
	from := ecrypto.PubkeyToAddress(s.deployer.PublicKey)
	input := hexutil.Bytes(data)
	gas, err := s.chain.estimateGas(simCallArgs{From: &from, To: to, Input: &input})
	c.Assert(err, IsNil)
	tx, err := etypes.SignNewTx(s.deployer, s.chain.signer, &etypes.LegacyTx{
		Nonce:    s.chain.pendingNonce(from),
		To:       to,
		Gas:      gas,
		GasPrice: big.NewInt(simGasRateGwei * 1e9),
		Data:     data,
	})
	c.Assert(err, IsNil)
	c.Assert(s.chain.sendTx(tx), IsNil)
	c.Assert(s.chain.commit(), IsNil)
	return s.mined(c, tx.Hash())
}

func (s *RouterSimSuite) mined(c *C, hash ecommon.Hash) *etypes.Receipt {
	receipt := s.chain.receipt(hash)
	c.Assert(receipt, NotNil)
	c.Assert(receipt.Status, Equals, etypes.ReceiptStatusSuccessful)
	return receipt
}

func (s *RouterSimSuite) deploy(c *C, bytecodeFile string) ecommon.Address {
	bytecode, err := os.ReadFile(bytecodeFile)
	c.Assert(err, IsNil)
	return s.send(c, nil, ecommon.FromHex(strings.TrimSpace(string(bytecode)))).ContractAddress
}

// erc20 calls a uint256 view of the test token.
func (s *RouterSimSuite) erc20(c *C, method string, args ...interface{}) *big.Int {
	erc20ABI := s.client.evmScanner.erc20ABI
	data, err := erc20ABI.Pack(method, args...)
	c.Assert(err, IsNil)
	input := hexutil.Bytes(data)
	out, err := s.chain.call(simCallArgs{To: &s.token, Input: &input})
	c.Assert(err, IsNil)
	values, err := erc20ABI.Unpack(method, out)
	c.Assert(err, IsNil)
	value, ok := values[0].(*big.Int)
	c.Assert(ok, Equals, true)
	return value
}

func (s *RouterSimSuite) outbound(asset common.Asset, amount uint64) stypes.TxOutItem {
	inHash := strings.Repeat("AB", 32)
	return stypes.TxOutItem{
		Chain:       common.BSCChain,
		ToAddress:   common.Address(simRecipient.Hex()),
		VaultPubKey: s.vaultPk,
		Coins:       common.Coins{common.NewCoin(asset, cosmos.NewUint(amount))},
		Memo:        "OUT:" + inHash,
		MaxGas:      common.Gas{common.NewCoin(common.BNBBEP20Asset, cosmos.NewUint(simMaxGas))},
		GasRate:     simGasRateGwei,
		InHash:      common.TxID(inHash),
	}
}

func (s *RouterSimSuite) broadcastAndMine(c *C, item stypes.TxOutItem, raw []byte) {
	txid, err := s.client.BroadcastTx(item, raw)
	c.Assert(err, IsNil)
	c.Assert(s.chain.commit(), IsNil)
	s.mined(c, ecommon.HexToHash(txid))
}

func (s *RouterSimSuite) TestNativeTransferOut(c *C) {
	item := s.outbound(common.BNBBEP20Asset, 2*common.One)

	// V6 transferOut reverts unless msg.value == amount, so the gas estimate, which
	// sends a placeholder value, must also encode that placeholder as the amount.
	raw, _, obs, err := s.client.SignTx(item, 1)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil, Commentf("SignTx skipped the outbound; the transferOut gas estimate reverted"))
	c.Assert(obs, NotNil)

	s.broadcastAndMine(c, item, raw)
	c.Check(s.chain.balance(simRecipient).String(), Equals, new(big.Int).Mul(big.NewInt(2), simEther).String())
	c.Check(s.chain.balance(s.router).Sign(), Equals, 0)
}

func (s *RouterSimSuite) TestERC20TransferOutApprovesRouterFirst(c *C) {
	funded := new(big.Int).Mul(big.NewInt(1000), simEther)
	data, err := s.client.evmScanner.erc20ABI.Pack("transfer", s.vault, funded)
	c.Assert(err, IsNil)
	s.send(c, &s.token, data)
	c.Assert(s.client.evmScanner.tokenManager.SaveTokenMeta("TKN", s.token.Hex(), 18), IsNil)
	c.Assert(s.erc20(c, "allowance", s.vault, s.router).Sign(), Equals, 0)

	asset, err := common.NewAsset("BSC.TKN-" + s.token.Hex())
	c.Assert(err, IsNil)
	item := s.outbound(asset, 10*common.One)
	amount := new(big.Int).Mul(big.NewInt(10), simEther)

	// V6 pulls tokens with transferFrom, which reverts until the vault approves the
	// router, so SignTx must broadcast the approval before it estimates gas for the
	// transferOut. Blocks are only mined on commit, so the approval is still pending
	// when SignTx estimates and this attempt defers the outbound for rescheduling.
	raw, _, _, err := s.client.SignTx(item, 1)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(s.chain.pendingNonce(s.vault), Equals, uint64(1), Commentf("SignTx did not broadcast the approval"))

	c.Assert(s.chain.commit(), IsNil)
	c.Assert(s.erc20(c, "allowance", s.vault, s.router).Cmp(amount) >= 0, Equals, true)

	// The rescheduled attempt signs after the approval's nonce and executes on chain.
	raw, _, obs, err := s.client.SignTx(item, 2)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil, Commentf("SignTx skipped the outbound after the approval was mined"))
	c.Assert(obs, NotNil)
	signed := &etypes.Transaction{}
	c.Assert(signed.UnmarshalJSON(raw), IsNil)
	c.Check(signed.Nonce(), Equals, uint64(1))

	s.broadcastAndMine(c, item, raw)
	c.Check(s.erc20(c, "balanceOf", simRecipient).String(), Equals, amount.String())
	c.Check(s.erc20(c, "balanceOf", s.vault).String(), Equals, new(big.Int).Sub(funded, amount).String())
}
