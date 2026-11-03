package providers

import (
	"context"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *ProviderTestSuite) TestBitstampProvider(c *C) {
	config, found := s.config[common.ProviderBitstamp]
	c.Assert(found, Equals, true)

	config.Pairs = []string{
		"BTC/USD",
		"ETH/USD",
		"RUJI/RUNE",
	}

	// poll every 10ms, not the suite's 1s, and stop when the test ends
	config.PollingInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	provider, err := NewBitstampProvider(ctx, s.logger, config, s.metrics)
	c.Assert(err, IsNil)
	c.Assert(provider, NotNil)
	c.Assert(provider.pairs, HasLen, 2)
	c.Assert(provider.tickers, HasLen, 0)

	symbol := provider.toMappedProviderSymbol(newPair("BTC/USD"))
	c.Assert(err, IsNil)
	c.Assert(symbol, Equals, "BTC/USD")

	// test polling

	provider.Start()

	waitForPrices(provider, 2, map[string]string{
		"BTC/USD": "109498.00",
		"ETH/USD": "2716.00",
	})

	tickers, err := provider.GetTickers()
	c.Assert(err, IsNil)
	c.Assert(tickers, HasLen, 2)

	ticker, found := tickers["BTC/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "109498.00")

	ticker, found = tickers["ETH/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "2716.00")
}
