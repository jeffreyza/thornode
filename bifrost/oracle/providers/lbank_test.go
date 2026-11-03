package providers

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *ProviderTestSuite) TestLbankProvider(c *C) {
	config, found := s.config[common.ProviderLbank]
	c.Assert(found, Equals, true)

	config.Pairs = []string{
		"BTC/USDT",
		"ETH/USDT",
		"RUJI/RUNE",
	}

	provider, err := NewLbankProvider(s.ctx, s.logger, config, s.metrics)
	c.Assert(err, IsNil)
	c.Assert(provider, NotNil)
	c.Assert(provider.pairs, HasLen, 2)
	c.Assert(provider.tickers, HasLen, 0)

	symbol := provider.toMappedProviderSymbol(newPair("BTC/USDT"))
	c.Assert(err, IsNil)
	c.Assert(symbol, Equals, "btc_usdt")

	// test polling

	provider.Poll()

	waitForPrices(provider, 2, map[string]string{
		"BTC/USDT": "109063.51",
		"ETH/USDT": "2656.33",
	})

	tickers, err := provider.GetTickers()
	c.Assert(err, IsNil)
	c.Assert(tickers, HasLen, 2)

	ticker, found := tickers["BTC/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "109063.51")

	ticker, found = tickers["ETH/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "2656.33")

	// test subscription msg

	str := `[{"action":"subscribe","pair":"btc_usdt","subscribe":"tick"},{"action":"subscribe","pair":"eth_usdt","subscribe":"tick"}]`

	msgs, err := provider.GetSubscriptionMsgs()
	c.Assert(err, IsNil)

	data, err := json.Marshal(msgs)
	c.Assert(err, IsNil)
	c.Assert(string(data), Equals, str)

	// test websocket

	// Start polls in background goroutines before connecting
	settleStartPoll(provider.websocketProvider, 2, map[string]string{
		"BTC/USDT": "109063.51",
		"ETH/USDT": "2656.33",
	})

	provider.Start()

	waitForPrices(provider, 2, map[string]string{
		"BTC/USDT": "109323.89",
		"ETH/USDT": "2663.29",
	})

	tickers, err = provider.GetTickers()
	c.Assert(err, IsNil)

	ticker, found = tickers["BTC/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "109323.89")

	ticker, found = tickers["ETH/USDT"]
	c.Assert(found, Equals, true)
	c.Assert(ticker.Price.Text('f', 2), Equals, "2663.29")
}
