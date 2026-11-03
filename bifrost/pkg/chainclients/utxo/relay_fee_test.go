package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/spf13/viper"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func TestValidateRelayFee(t *testing.T) {
	for _, chain := range []common.Chain{common.BTCChain, common.LTCChain, common.BCHChain, common.DOGEChain, common.DASHChain} {
		t.Run(chain.String(), func(t *testing.T) {
			client := &Client{cfg: config.BifrostChainConfiguration{ChainID: chain}}
			for _, tc := range []struct {
				name       string
				relay, fee uint64
				size       int64
				wantErr    bool
			}{
				{"below minimum", 1000, 1932, 1933, true},
				{"exact minimum", 1000, 1933, 1933, false},
				{"above minimum", 1000, 2000, 1933, false},
				{"sub-kilobyte size", 1000, 250, 250, false},
				{"fractional minimum rounded up", 1001, 1002, 1001, true},
				{"rounded minimum met", 1001, 1003, 1001, false},
				{"zero relay rate", 0, 0, 1000, false},
				{"zero size", 1000, 1000, 0, true},
				{"negative size", 1000, 1000, -1, true},
				{"large product does not overflow", math.MaxUint64, math.MaxUint64, 1000, false},
				{"minimum exceeds uint64", math.MaxUint64, math.MaxUint64, 1001, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					client.minRelayFeeSats.Store(tc.relay)
					if err := client.validateRelayFee(tc.fee, tc.size); (err != nil) != tc.wantErr {
						t.Fatalf("validateRelayFee: got %v, want error %v", err, tc.wantErr)
					}
				})
			}
		})
	}
	// ZEC's ZIP-317 fee must not be compared against a byte-based minimum.
	zec := &Client{cfg: config.BifrostChainConfiguration{ChainID: common.ZECChain}}
	zec.minRelayFeeSats.Store(1000)
	if err := zec.validateRelayFee(1, 2000); err != nil {
		t.Fatal(err)
	}
}

type relayFeeBridge struct{ thorclient.ThorchainBridge }

// Use the configured consolidation target, rather than a Mimir override.
func (relayFeeBridge) GetMimir(string) (int64, error) { return 0, nil }

func (s *DashSuite) TestDashBuildTxRelayFee(c *C) {
	vault, err := common.NewPubKeyFromCrypto(secp256k1.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	address, err := vault.GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	sourceScript, err := newDashClientForUnitTest().getSourceScript(stypes.TxOutItem{VaultPubKey: vault})
	c.Assert(err, IsNil)

	// Read the shipped config locally, without changing the global test config.
	defaults, err := os.ReadFile("../../../../config/default.yaml")
	c.Assert(err, IsNil)
	v := viper.New()
	v.SetConfigType("yaml")
	c.Assert(v.ReadConfig(bytes.NewReader(defaults)), IsNil)
	var cfg config.BifrostChainConfiguration
	c.Assert(v.UnmarshalKey("bifrost.chains.dash", &cfg), IsNil)

	for _, tc := range []struct {
		name        string
		inputs      int
		maxGas      uint64
		gasRate     int64
		migrate     bool
		consolidate bool
		wantFee     int64
		wantErr     bool
	}{
		{name: "old budget rejected", inputs: 12, maxGas: 1800, gasRate: 6, wantErr: true},
		{name: "one duff below minimum", inputs: 12, maxGas: 1932, gasRate: 6, wantErr: true},
		{name: "exact minimum", inputs: 12, maxGas: 1933, gasRate: 6, wantFee: 1933},
		{name: "new budget accepts twelve inputs", inputs: 12, maxGas: 6000, gasRate: 3, wantFee: 5799},
		{name: "new budget still checked for larger transactions", inputs: 40, maxGas: 6000, gasRate: 3, wantErr: true},
		{name: "larger budget accepts forty inputs", inputs: 40, maxGas: 10000, gasRate: 3, wantFee: 10000},
		{name: "migration below minimum", inputs: 12, maxGas: 1839, gasRate: 6, migrate: true, wantErr: true},
		{name: "migration uses no-change size", inputs: 12, maxGas: 1840, gasRate: 6, migrate: true, wantFee: 1840},
		{name: "consolidation without MaxGas", inputs: 12, consolidate: true},
	} {
		c.Log(tc.name)
		utxos := make([]btcjson.ListUnspentResult, tc.inputs)
		for i := range utxos {
			utxos[i] = btcjson.ListUnspentResult{
				TxID: fmt.Sprintf("%064x", i+1), Amount: 1,
				ScriptPubKey:  hex.EncodeToString(sourceScript),
				Confirmations: 10, Spendable: true,
			}
		}
		server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
			switch method {
			case "listunspent":
				return utxos
			case "getblockhash":
				return strings.Repeat("a", 64)
			case "getblockstats":
				return map[string]any{"avgfeerate": 1}
			default:
				c.Errorf("unexpected RPC %s", method)
				return nil
			}
		})
		client, cleanup := newDashClientForRPCServer(c, server.URL)
		func() {
			defer server.Close()
			defer cleanup()
			client.cfg = cfg
			client.bridge = relayFeeBridge{}
			client.m = GetMetricForTest(c, common.DASHChain)
			feeQueue := make(chan common.NetworkFee, 1)
			client.globalNetworkFeeQueue = feeQueue
			client.minRelayFeeSats.Store(1000)
			c.Assert(client.sendNetworkFee(100), IsNil)
			// Inspect the actual report used to establish THORChain's fee budget.
			reportedFee := <-feeQueue
			c.Assert(reportedFee.TransactionRate, Equals, uint64(2))
			c.Assert(reportedFee.TransactionSize, Equals, uint64(2000))
			c.Assert(client.cfg.UTXO.MaxUTXOsToSpend, Equals, int64(10))

			total := uint64(tc.inputs) * common.One
			payment := total - common.One/2 // requires every input despite the target of 10
			memo := "OUT:" + strings.Repeat("a", 64)
			if tc.migrate {
				payment = total - tc.maxGas
				memo = "MIGRATE:1"
			}
			tx := stypes.TxOutItem{
				Chain: common.DASHChain, VaultPubKey: vault, ToAddress: address,
				Coins:   common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(payment))),
				MaxGas:  common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(tc.maxGas))},
				GasRate: tc.gasRate, Memo: memo,
			}
			if tc.consolidate {
				tx, err = client.buildConsolidateTxOutItem(ttypes.Vault{PubKey: vault}, utxos)
				c.Assert(err, IsNil)
				c.Assert(tx.MaxGas.IsEmpty(), Equals, true)
			}
			built, _, buildErr := client.buildTx(tx, sourceScript, 0)
			if tc.wantErr {
				c.Assert(buildErr, ErrorMatches, "DASH fee .* is below relay minimum .*")
				c.Assert(built, IsNil)
				return
			}
			c.Assert(buildErr, IsNil)
			c.Assert(built.TxIn, HasLen, tc.inputs)
			fee := int64(total)
			for _, output := range built.TxOut {
				fee -= output.Value
			}
			if !tc.consolidate {
				c.Assert(fee, Equals, tc.wantFee)
			}
			if tc.migrate || tc.consolidate {
				c.Assert(built.TxOut, HasLen, 2) // destination and memo; no change
			} else {
				c.Assert(built.TxOut, HasLen, 3)
			}
		}()
	}
}
