package evm

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"net/http/httptest"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/gasestimator"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/triedb"
)

// simChain is a minimal in-process EVM chain for chain client tests: geth's state
// transition and gas estimator over an in-memory state, served as JSON-RPC by a
// geth rpc.Server. Transactions wait in the pending list until commit mines them
// into the next block. It implements only the eth_ methods EVMClient uses to sign
// and broadcast; anything else returns "method not found".
//
// go-ethereum's ethclient/simulated would be the natural choice, but it imports the
// full node (node, eth, catalyst), which needs modules THORNode's go.mod does not
// list. Everything used here is already in THORNode's build graph.
type simChain struct {
	mu       sync.Mutex
	config   *params.ChainConfig
	signer   etypes.Signer
	state    *state.StateDB // state after the last committed block
	headers  []*etypes.Header
	pending  []*etypes.Transaction
	receipts map[ecommon.Hash]*etypes.Receipt
	server   *httptest.Server
}

const simBlockGasLimit = 30_000_000

func newSimChain(alloc etypes.GenesisAlloc) (*simChain, error) {
	genesis := &core.Genesis{
		Config:   params.AllDevChainProtocolChanges, // chain id 1337, every fork through Prague
		GasLimit: simBlockGasLimit,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc:    alloc,
	}
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	block, err := genesis.Commit(db, tdb)
	if err != nil {
		return nil, err
	}
	statedb, err := state.New(block.Root(), state.NewDatabase(tdb, nil))
	if err != nil {
		return nil, err
	}
	c := &simChain{
		config:   genesis.Config,
		signer:   etypes.LatestSigner(genesis.Config),
		state:    statedb,
		headers:  []*etypes.Header{block.Header()},
		receipts: map[ecommon.Hash]*etypes.Receipt{},
	}
	srv := rpc.NewServer()
	if err = srv.RegisterName("eth", &simEthAPI{c}); err != nil {
		return nil, err
	}
	c.server = httptest.NewServer(srv)
	return c, nil
}

func (c *simChain) close() { c.server.Close() }

// URL is the JSON-RPC endpoint.
func (c *simChain) URL() string { return c.server.URL }

// core.ChainContext. GetHeader runs inside EVM execution, which holds mu.
func (c *simChain) Engine() consensus.Engine    { return ethash.NewFaker() }
func (c *simChain) Config() *params.ChainConfig { return c.config }
func (c *simChain) GetHeader(hash ecommon.Hash, number uint64) *etypes.Header {
	if number < uint64(len(c.headers)) && c.headers[number].Hash() == hash {
		return c.headers[number]
	}
	return nil
}

func (c *simChain) head() *etypes.Header { return c.headers[len(c.headers)-1] }

// commit mines every pending transaction into a new block and fails if any of them
// cannot be included. The base fee stays at its genesis value.
func (c *simChain) commit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	parent := c.head()
	header := &etypes.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, ecommon.Big1),
		GasLimit:   parent.GasLimit,
		Time:       parent.Time + 1,
		Difficulty: new(big.Int),
		BaseFee:    new(big.Int).Set(parent.BaseFee),
	}
	evm := vm.NewEVM(core.NewEVMBlockContext(header, c, nil, c.config, c.state), c.state, c.config, vm.Config{})
	gp := new(core.GasPool).AddGas(header.GasLimit)
	for i, tx := range c.pending {
		c.state.SetTxContext(tx.Hash(), i)
		receipt, err := core.ApplyTransaction(evm, gp, c.state, header, tx, &header.GasUsed)
		if err != nil {
			return fmt.Errorf("tx %s: %w", tx.Hash(), err)
		}
		c.receipts[tx.Hash()] = receipt
	}
	c.headers = append(c.headers, header)
	c.pending = nil
	return nil
}

// nonce returns the account nonce at the head block, or including pending txs.
func (c *simChain) nonce(addr ecommon.Address, pending bool) uint64 {
	nonce := c.state.GetNonce(addr)
	if !pending {
		return nonce
	}
	for _, tx := range c.pending {
		if from, _ := etypes.Sender(c.signer, tx); from == addr && tx.Nonce() >= nonce {
			nonce = tx.Nonce() + 1
		}
	}
	return nonce
}

// sendTx queues a signed transaction, accepting only the sender's next nonce.
func (c *simChain) sendTx(tx *etypes.Transaction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	from, err := etypes.Sender(c.signer, tx)
	if err != nil {
		return err
	}
	for _, queued := range c.pending {
		if queued.Hash() == tx.Hash() {
			return txpool.ErrAlreadyKnown
		}
	}
	if next := c.nonce(from, true); tx.Nonce() != next {
		if tx.Nonce() < next {
			return fmt.Errorf("%w: address %s, tx: %d next: %d", core.ErrNonceTooLow, from, tx.Nonce(), next)
		}
		return fmt.Errorf("nonce gap: address %s, tx: %d next: %d", from, tx.Nonce(), next)
	}
	c.pending = append(c.pending, tx)
	return nil
}

func (c *simChain) receipt(hash ecommon.Hash) *etypes.Receipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.receipts[hash]
}

func (c *simChain) balance(addr ecommon.Address) *big.Int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.GetBalance(addr).ToBig()
}

func (c *simChain) pendingNonce(addr ecommon.Address) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nonce(addr, true)
}

// call executes a message against the head state, as eth_call at "latest".
func (c *simChain) call(args simCallArgs) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	head := c.head()
	msg := args.message(head.GasLimit)
	blockCtx := core.NewEVMBlockContext(head, c, nil, c.config, c.state)
	if msg.GasPrice.Sign() == 0 {
		blockCtx.BaseFee = new(big.Int)
	}
	evm := vm.NewEVM(blockCtx, c.state.Copy(), c.config, vm.Config{NoBaseFee: true})
	result, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(math.MaxUint64))
	if err != nil {
		return nil, err
	}
	if len(result.Revert()) > 0 {
		return nil, newSimRevertError(result.Revert())
	}
	return result.Return(), result.Err
}

// estimateGas runs geth's own eth_estimateGas binary search against the head state.
func (c *simChain) estimateGas(args simCallArgs) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	opts := &gasestimator.Options{Config: c.config, Chain: c, Header: c.head(), State: c.state.Copy(), ErrorRatio: 0.015}
	gas, revert, err := gasestimator.Estimate(context.Background(), args.message(0), opts, simBlockGasLimit)
	if len(revert) > 0 {
		return 0, newSimRevertError(revert)
	}
	return gas, err
}

// simCallArgs is the subset of the eth_call / eth_estimateGas transaction object
// that ethclient sends.
type simCallArgs struct {
	From     *ecommon.Address `json:"from"`
	To       *ecommon.Address `json:"to"`
	Gas      *hexutil.Uint64  `json:"gas"`
	GasPrice *hexutil.Big     `json:"gasPrice"`
	Value    *hexutil.Big     `json:"value"`
	Data     *hexutil.Bytes   `json:"data"`
	Input    *hexutil.Bytes   `json:"input"`
}

func (a simCallArgs) message(defaultGas uint64) *core.Message {
	msg := &core.Message{
		To:               a.To,
		GasLimit:         defaultGas,
		GasPrice:         new(big.Int),
		GasFeeCap:        new(big.Int),
		GasTipCap:        new(big.Int),
		Value:            new(big.Int),
		SkipNonceChecks:  true,
		SkipFromEOACheck: true,
	}
	if a.From != nil {
		msg.From = *a.From
	}
	if a.Gas != nil {
		msg.GasLimit = uint64(*a.Gas)
	}
	if a.GasPrice != nil {
		msg.GasPrice, msg.GasFeeCap, msg.GasTipCap = a.GasPrice.ToInt(), a.GasPrice.ToInt(), a.GasPrice.ToInt()
	}
	if a.Value != nil {
		msg.Value = a.Value.ToInt()
	}
	switch {
	case a.Input != nil:
		msg.Data = *a.Input
	case a.Data != nil:
		msg.Data = *a.Data
	}
	return msg
}

// simRevertError matches geth's JSON-RPC revert error: code 3, reason in the
// message, raw revert data in the error data.
type simRevertError struct{ msg, data string }

func newSimRevertError(revert []byte) *simRevertError {
	msg := vm.ErrExecutionReverted.Error()
	if reason, err := abi.UnpackRevert(revert); err == nil {
		msg += ": " + reason
	}
	return &simRevertError{msg: msg, data: hexutil.Encode(revert)}
}

func (e *simRevertError) Error() string          { return e.msg }
func (e *simRevertError) ErrorCode() int         { return 3 }
func (e *simRevertError) ErrorData() interface{} { return e.data }

// simEthAPI is the "eth" JSON-RPC namespace of a simChain. Block arguments are
// accepted for wire compatibility; everything reads the head block except the
// "pending" transaction count.
type simEthAPI struct{ c *simChain }

func (api *simEthAPI) ChainId() *hexutil.Big { return (*hexutil.Big)(api.c.config.ChainID) }

func (api *simEthAPI) BlockNumber() hexutil.Uint64 {
	api.c.mu.Lock()
	defer api.c.mu.Unlock()
	return hexutil.Uint64(api.c.head().Number.Uint64())
}

func (api *simEthAPI) GetBalance(addr ecommon.Address, _ *rpc.BlockNumberOrHash) *hexutil.Big {
	return (*hexutil.Big)(api.c.balance(addr))
}

func (api *simEthAPI) GetTransactionCount(addr ecommon.Address, block *rpc.BlockNumberOrHash) hexutil.Uint64 {
	pending := false
	if block != nil {
		number, ok := block.Number()
		pending = ok && number == rpc.PendingBlockNumber
	}
	api.c.mu.Lock()
	defer api.c.mu.Unlock()
	return hexutil.Uint64(api.c.nonce(addr, pending))
}

func (api *simEthAPI) Call(args simCallArgs, _ *rpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	return api.c.call(args)
}

func (api *simEthAPI) EstimateGas(args simCallArgs, _ *rpc.BlockNumberOrHash) (hexutil.Uint64, error) {
	gas, err := api.c.estimateGas(args)
	return hexutil.Uint64(gas), err
}

func (api *simEthAPI) SendRawTransaction(input hexutil.Bytes) (ecommon.Hash, error) {
	tx := new(etypes.Transaction)
	if err := tx.UnmarshalBinary(input); err != nil {
		return ecommon.Hash{}, err
	}
	if err := api.c.sendTx(tx); err != nil {
		return ecommon.Hash{}, err
	}
	return tx.Hash(), nil
}
