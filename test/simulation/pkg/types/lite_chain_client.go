package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

// SimTx is a struct used for simulation transactions.
type SimTx struct {
	Chain     common.Chain
	ToAddress common.Address
	Coin      common.Coin
	Memo      string
	// OneShotMemo prevents fresh replacement transactions after a chain accepts
	// a txid for memo flows that consume one-time THORChain state.
	OneShotMemo bool

	// Optional fee override for UTXO-style chains, in satoshis/duffs.
	// If zero, the client will fall back to its chain defaults.
	FeeSats uint64
}

// SimContractTx is a struct used for simulation contract call transactions.
type SimContractTx struct {
	Chain    common.Chain
	Contract common.Address
	ABI      abi.ABI
	Method   string
	Args     []interface{}
	Value    *big.Int
}

// LiteChainClient is a subset of the ChainClient interface used for simulation tests.
type LiteChainClient interface {
	// GetAccount returns the account for the given public key. If the key is nil, it
	// returns the account for the client's configured key.
	GetAccount(_ *common.PubKey) (*common.Account, error)

	// SignTx returns the signed transaction.
	SignTx(tx SimTx) ([]byte, error)

	// BroadcastTx broadcasts the transaction and returns the hash.
	BroadcastTx([]byte) (string, error)
}

// LiteChainClientConstructor is a function that creates a new LiteChainClient.
type LiteChainClientConstructor func(_ common.Chain, _ *thorclient.Keys) (LiteChainClient, error)
