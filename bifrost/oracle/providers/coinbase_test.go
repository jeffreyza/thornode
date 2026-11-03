package providers

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *ProviderTestSuite) TestCoinbaseProvider(c *C) {
	config, found := s.config[common.ProviderCoinbase]
	c.Assert(found, Equals, true)

	config.Pairs = []string{
		"BTC/USD",
		"ETH/USD",
		"RUJI/RUNE",
	}

	provider, err := NewCoinbaseProvider(s.ctx, s.logger, config, s.metrics)
	c.Assert(err, IsNil)
	c.Assert(provider, NotNil)
	c.Assert(provider.pairs, HasLen, 2)
	c.Assert(provider.tickers, HasLen, 0)

	symbol := provider.toMappedProviderSymbol(newPair("BTC/USD"))
	c.Assert(err, IsNil)
	c.Assert(symbol, Equals, "BTC-USD")

	// test polling

	provider.Poll()

	waitForPrices(provider, 2, map[string]string{
		"BTC/USD": "108769.08",
		"ETH/USD": "2613.33",
	})

	tickers, err := provider.GetTickers()
	c.Assert(err, IsNil)
	c.Assert(tickers, HasLen, 2)

	ticker, found := tickers["BTC/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "108769.08")

	ticker, found = tickers["ETH/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "2613.33")

	// test subscription msg

	str := `[{"channels":["ticker"],"product_ids":["BTC-USD","ETH-USD"],"type":"subscribe"}]`

	msgs, err := provider.GetSubscriptionMsgs()
	c.Assert(err, IsNil)

	data, err := json.Marshal(msgs)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, str)

	// test websocket

	// Start polls in background goroutines before connecting
	settleStartPoll(provider.websocketProvider, 2, map[string]string{
		"BTC/USD": "108769.08",
		"ETH/USD": "2613.33",
	})

	provider.Start()

	waitForPrices(provider, 2, map[string]string{
		"BTC/USD": "108741.64",
		"ETH/USD": "2610.05",
	})

	tickers, err = provider.GetTickers()
	c.Assert(err, IsNil)

	ticker, found = tickers["BTC/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "108741.64")

	ticker, found = tickers["ETH/USD"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "2610.05")
}
