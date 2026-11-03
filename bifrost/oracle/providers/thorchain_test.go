package providers

import (
	"context"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *ProviderTestSuite) TestThorchainProvider(c *C) {
	config, found := s.config[common.ProviderThorchain]
	c.Assert(found, Equals, true)

	config.Pairs = []string{
		"RUNE/USD",
		"RUJI/RUNE",
	}

	// poll every 10ms, not the suite's 1s, and stop when the test ends
	config.PollingInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	provider, err := NewThorchainProvider(ctx, s.logger, config, s.metrics)
	c.Assert(err, IsNil)
	c.Assert(provider, NotNil)
	c.Assert(provider.pairs, HasLen, 1)
	c.Assert(provider.tickers, HasLen, 0)

	symbol := provider.toMappedProviderSymbol(newPair("RUNE/USD"))
	c.Assert(err, IsNil)
	c.Assert(symbol, Equals, "RUNEUSD")

	// test polling

	provider.Start()

	waitForPrices(provider, 4, map[string]string{
		"RUNE/USD": "1.2486",
	})

	tickers, err := provider.GetTickers()
	c.Assert(err, IsNil)
	c.Assert(tickers, HasLen, 1)

	ticker, found := tickers["RUNE/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 4), Equals, "1.2486")
}
