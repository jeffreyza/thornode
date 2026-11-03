package utxo

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// useListUnspent points the client RPC at a server that answers listunspent with
// the given result and fails every other method. The caller closes the server.
func (s *DogecoinSignerSuite) useListUnspent(c *C, result string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&r), IsNil)
		rw.Header().Set("Content-Type", "application/json")
		reply := `"result":` + result
		if r.Method != "listunspent" {
			reply = `"error":{"code":-5,"message":"No such mempool or blockchain transaction"}`
		}
		_, err := fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":%s,%s}`, r.ID, reply)
		c.Assert(err, IsNil)
	}))
	client, err := rpc.NewClient(server.URL, bob, password, 0, 0, common.DOGEChain, zerolog.Nop())
	c.Assert(err, IsNil)
	s.client.rpc = client
	return server
}

// vaultScript returns the address and output script of the vault the selection
// tests fund. The node key is used so the pubkey prefix matches the build network.
func (s *DogecoinSignerSuite) vaultScript(c *C) (common.PubKey, common.Address, string) {
	vault := s.client.nodePubKey
	addr, err := vault.GetAddress(common.DOGEChain)
	c.Assert(err, IsNil)
	script, err := s.client.getSourceScript(stypes.TxOutItem{VaultPubKey: vault, Chain: common.DOGEChain})
	c.Assert(err, IsNil)
	return vault, addr, hex.EncodeToString(script)
}

func (s *DogecoinSignerSuite) TestUTXOShortfallReportsCoinsAbsentFromNode(c *C) {
	vault, addr, _ := s.vaultScript(c)
	defer s.useListUnspent(c, `[]`).Close()

	_, err := s.client.getUtxoToSpend(vault, btcutil.Amount(1224487968695), 0)
	c.Check(err, ErrorMatches, `insufficient available UTXOs: need 1224487968695, node lists no UTXOs for `+addr.String()+` .*coins absent.*`)
}

func (s *DogecoinSignerSuite) TestUTXOShortfallReportsListedButExcludedCoins(c *C) {
	vault, addr, script := s.vaultScript(c)
	s.client.cfg.UTXO.MinUTXOConfirmations = 1
	defer s.useListUnspent(c, `[
		{"txid":"`+fakeTxID(1)+`","vout":0,"address":"`+addr.String()+`","scriptPubKey":"`+script+`","amount":5000,"confirmations":0,"spendable":true},
		{"txid":"`+fakeTxID(2)+`","vout":0,"address":"`+addr.String()+`","scriptPubKey":"6a","amount":7000,"confirmations":10,"spendable":true}
	]`).Close()

	_, err := s.client.getUtxoToSpend(vault, btcutil.Amount(1224487968695), 0)
	c.Check(err, ErrorMatches, `insufficient available UTXOs: need 1224487968695, only have 0 available from 0 UTXOs; `+
		`node lists 2 UTXOs worth 1200000000000 for `+addr.String()+`, excluded: invalid script=1, `+
		`unconfirmed or dust not from asgard=1, mempool chain limit=0, spent check failed=0, already spent=0`)
}

func (s *DogecoinSignerSuite) TestUTXOSelectionSpendsListedConfirmedCoins(c *C) {
	vault, addr, script := s.vaultScript(c)
	s.client.cfg.UTXO.MinUTXOConfirmations = 1
	defer s.useListUnspent(c, `[
		{"txid":"`+fakeTxID(1)+`","vout":0,"address":"`+addr.String()+`","scriptPubKey":"`+script+`","amount":12244.87968695,"confirmations":3,"spendable":true}
	]`).Close()

	utxos, err := s.client.getUtxoToSpend(vault, btcutil.Amount(1224487968695), 0)
	c.Assert(err, IsNil)
	c.Assert(utxos, HasLen, 1)
	c.Check(utxos[0].TxID, Equals, fakeTxID(1))
}

func fakeTxID(n int) string {
	return fmt.Sprintf("%064x", n)
}
