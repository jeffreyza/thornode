package thorclient

import (
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	hd "github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

var m *metrics.Metrics

func SetupThorchainForTest(c *C) (config.BifrostClientConfiguration, *cKeys.Record, cKeys.Keyring) {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg.SetBech32PrefixForValidator("tthorv", "tthorvpub")
	cfg.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
	cfg.SetCoinType(cmd.THORChainCoinType)
	cfg.SetPurpose(cmd.THORChainCoinPurpose)
	clientCfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		ChainRPC:        "localhost",
		SignerName:      "thorchain",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)

	params := *hd.NewFundraiserParams(0, sdk.CoinType, 0)
	hdPath := params.String()

	// create a consistent user
	record, err := kb.NewAccount(clientCfg.SignerName, "industry segment educate height inject hover bargain offer employ select speak outer video tornado story slow chief object junk vapor venue large shove behave", clientCfg.SignerPasswd, hdPath, hd.Secp256k1)
	c.Assert(err, IsNil)

	return clientCfg, record, kb
}

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.ETHChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}
