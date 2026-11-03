package providers

import (
	"time"

	"github.com/coder/websocket"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

// defaultWsSubscribeDelay is the production pause, read before init zeroes it.
var defaultWsSubscribeDelay = wsSubscribeDelay

// Each provider test connects to the mock websocket server; skip the real
// settle wait after subscribing and the pause between subscription messages
// (the tests still wait for the messages).
func init() {
	wsSettleDelay = 0
	wsSubscribeDelay = 0
}

func (s *ProviderTestSuite) TestSubscribeUsesSubscribeDelay(c *C) {
	provider, err := NewBinanceProvider(s.ctx, s.logger, s.config[common.ProviderBinance], s.metrics)
	c.Assert(err, IsNil)

	msgs := make([]map[string]any, 5)
	for i := range msgs {
		msgs[i] = map[string]any{"id": i}
	}
	provider.getSubs = func() ([]map[string]any, error) { return msgs, nil }

	provider.ws, _, err = websocket.Dial(s.ctx, provider.wsEndpoint, nil)
	c.Assert(err, IsNil)
	defer func() { _ = provider.ws.CloseNow() }()

	// the production pause alone would hold five messages for four delays
	start := time.Now()
	c.Assert(provider.subscribe(), IsNil)
	c.Assert(time.Since(start) < 4*defaultWsSubscribeDelay, Equals, true)
	c.Assert(defaultWsSubscribeDelay, Equals, 100*time.Millisecond)
}
