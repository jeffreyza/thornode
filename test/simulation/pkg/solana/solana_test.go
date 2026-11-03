package solana

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	. "gopkg.in/check.v1"
)

func Test(t *testing.T) { TestingT(t) }

type SolanaSuite struct{}

var _ = Suite(&SolanaSuite{})

func (s *SolanaSuite) TestClient(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result any
		switch request.Method {
		case "getLatestBlockhash":
			result = map[string]any{"value": map[string]any{"blockhash": "11111111111111111111111111111111"}}
		case "sendTransaction":
			result = "local-signature"
		case "getTransaction":
			result = map[string]any{"slot": 1, "meta": map[string]any{"err": nil}}
		default:
			http.Error(w, "unexpected RPC method: "+request.Method, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer server.Close()

	mnemonic := "master master master master master master master master master master master master master master master master master master master master master master master notice"

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	// Add ed25519 key to keyring
	kr := keyring.NewInMemory(cdc, func(options *keyring.Options) {
		options.SupportedAlgos = keyring.SigningAlgoList{ed25519.Ed25519}
	})
	name := strings.Split(mnemonic, " ")[0]
	edName := fmt.Sprintf("ed-%s", name)
	_, err := kr.NewAccount(edName, mnemonic, "", ed25519.HDPath, ed25519.Ed25519)
	c.Assert(err, IsNil)

	// create thorclient.Keys for chain client construction
	keys := thorclient.NewKeysWithKeybase(kr, name, "")

	client, err := NewClient(common.SOLChain, server.URL, keys)
	c.Assert(err, IsNil)
	c.Assert(client, NotNil)

	simTx := types.SimTx{
		Chain:     common.SOLChain,
		ToAddress: common.Address("6oEorfnzTgD4qa9G11SyuCxoudZT1Y44bnErCbhC7RQT"),
		Coin:      common.NewCoin(common.SOLAsset, cosmos.NewUint(10000)),
		Memo:      "SIMULATION:hawk",
	}

	// Sign tx
	sig, err := client.SignTx(simTx)
	c.Assert(err, IsNil)
	c.Assert(sig, NotNil)

	// Broadcast tx
	hash, err := client.BroadcastTx(sig)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, "local-signature")
}
