package common

import (
	"bytes"
	"testing"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// A proposer may carry an honest signature with a different message. Each row
// changes one security-relevant field while retaining the original signature.
func TestQuorumPayloadSecurityMatrix(t *testing.T) {
	key := secp256k1.GenPrivKey()
	observed := ObservedTx{
		Tx: Tx{
			ID: "tx-one", Chain: BTCChain, FromAddress: "from", ToAddress: "to",
			Coins: NewCoins(NewCoin(BTCAsset, cosmos.NewUint(100))),
			Gas:   Gas{NewCoin(BTCAsset, cosmos.NewUint(2))}, Memo: "memo",
		},
		BlockHeight: 100, FinaliseHeight: 106, ObservedPubKey: "vault-one",
	}
	fee := NetworkFee{Chain: BTCChain, Height: 100, TransactionRate: 12, TransactionSize: 200}
	solvency := Solvency{
		Id: "solvency-one", Chain: BTCChain, PubKey: "vault-one",
		Coins: NewCoins(NewCoin(BTCAsset, cosmos.NewUint(100))), Height: 100,
	}
	errata := ErrataTx{Id: "tx-one", Chain: BTCChain}
	price := PriceFeed{
		Version: []byte("version1"), Time: 1000,
		Rates: []*OraclePrice{{Amount: 100, Decimals: 2}},
	}

	type variant struct {
		name    string
		payload func() ([]byte, error)
	}
	type family struct {
		name     string
		base     func() ([]byte, error)
		variants []variant
	}
	families := []family{
		{"observed", func() ([]byte, error) { return observed.GetSignablePayloadWithInbound(true) }, []variant{
			{"direction", func() ([]byte, error) { return observed.GetSignablePayloadWithInbound(false) }},
			{"chain", func() ([]byte, error) {
				x := observed
				x.Tx.Chain = ETHChain
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"tx id", func() ([]byte, error) {
				x := observed
				x.Tx.ID = "tx-two"
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"from", func() ([]byte, error) {
				x := observed
				x.Tx.FromAddress = "other"
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"to", func() ([]byte, error) {
				x := observed
				x.Tx.ToAddress = "other"
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"coins", func() ([]byte, error) {
				x := observed
				x.Tx.Coins = NewCoins(NewCoin(BTCAsset, cosmos.NewUint(101)))
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"gas", func() ([]byte, error) {
				x := observed
				x.Tx.Gas = Gas{NewCoin(BTCAsset, cosmos.NewUint(3))}
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"memo", func() ([]byte, error) {
				x := observed
				x.Tx.Memo = "OUT:other"
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"height", func() ([]byte, error) { x := observed; x.BlockHeight++; return x.GetSignablePayloadWithInbound(true) }},
			{"finality", func() ([]byte, error) {
				x := observed
				x.FinaliseHeight = x.BlockHeight
				return x.GetSignablePayloadWithInbound(true)
			}},
			{"vault", func() ([]byte, error) {
				x := observed
				x.ObservedPubKey = "vault-two"
				return x.GetSignablePayloadWithInbound(true)
			}},
		}},
		{"network fee", fee.GetSignablePayload, []variant{
			{"chain", func() ([]byte, error) { x := fee; x.Chain = ETHChain; return x.GetSignablePayload() }},
			{"height", func() ([]byte, error) { x := fee; x.Height++; return x.GetSignablePayload() }},
			{"rate", func() ([]byte, error) { x := fee; x.TransactionRate++; return x.GetSignablePayload() }},
			{"size", func() ([]byte, error) { x := fee; x.TransactionSize++; return x.GetSignablePayload() }},
		}},
		{"solvency", solvency.GetSignablePayload, []variant{
			{"id", func() ([]byte, error) { x := solvency; x.Id = "solvency-two"; return x.GetSignablePayload() }},
			{"chain", func() ([]byte, error) { x := solvency; x.Chain = ETHChain; return x.GetSignablePayload() }},
			{"vault", func() ([]byte, error) { x := solvency; x.PubKey = "vault-two"; return x.GetSignablePayload() }},
			{"coins", func() ([]byte, error) {
				x := solvency
				x.Coins = NewCoins(NewCoin(BTCAsset, cosmos.NewUint(101)))
				return x.GetSignablePayload()
			}},
			{"height", func() ([]byte, error) { x := solvency; x.Height++; return x.GetSignablePayload() }},
		}},
		{"errata", errata.GetSignablePayload, []variant{
			{"id", func() ([]byte, error) { x := errata; x.Id = "tx-two"; return x.GetSignablePayload() }},
			{"chain", func() ([]byte, error) { x := errata; x.Chain = ETHChain; return x.GetSignablePayload() }},
		}},
		{"price feed", price.GetSignablePayload, []variant{
			{"version", func() ([]byte, error) { x := price; x.Version = []byte("version2"); return x.GetSignablePayload() }},
			{"time", func() ([]byte, error) { x := price; x.Time++; return x.GetSignablePayload() }},
			{"amount", func() ([]byte, error) {
				x := price
				x.Rates = []*OraclePrice{{Amount: 101, Decimals: 2}}
				return x.GetSignablePayload()
			}},
			{"decimals", func() ([]byte, error) {
				x := price
				x.Rates = []*OraclePrice{{Amount: 100, Decimals: 3}}
				return x.GetSignablePayload()
			}},
		}},
	}

	for _, f := range families {
		t.Run(f.name, func(t *testing.T) {
			original, err := f.base()
			if err != nil {
				t.Fatal(err)
			}
			sig, err := key.Sign(original)
			if err != nil || !key.PubKey().VerifySignature(original, sig) {
				t.Fatalf("valid signature: %v", err)
			}
			for _, v := range f.variants {
				t.Run(v.name, func(t *testing.T) {
					changed, err := v.payload()
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Equal(original, changed) || key.PubKey().VerifySignature(changed, sig) {
						t.Fatal("altered payload accepted original attestation")
					}
				})
			}
		})
	}
}
