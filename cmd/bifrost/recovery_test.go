package main

import (
	"context"
	"errors"
	"time"

	. "gopkg.in/check.v1"
)

type RecoverySuite struct{}

var _ = Suite(&RecoverySuite{})

func (s *RecoverySuite) TestRetryRecoveryUntilDoneRetriesWithCappedBackoff(c *C) {
	attempts := 0
	attempt := func() error {
		attempts++
		if attempts < 3 {
			return errors.New("still unrecoverable")
		}
		return nil
	}

	var nextDelays []time.Duration
	onFailure := func(err error, n int, next time.Duration) {
		c.Check(err, NotNil)
		c.Check(n, Equals, len(nextDelays)+1)
		nextDelays = append(nextDelays, next)
	}

	// initial=1ms doubles to 2ms, then caps at maxDelay=3ms.
	recovered := retryRecoveryUntilDone(context.Background(), time.Millisecond, 3*time.Millisecond, attempt, onFailure)

	c.Check(recovered, Equals, true)
	c.Check(attempts, Equals, 3)
	c.Check(nextDelays, DeepEquals, []time.Duration{2 * time.Millisecond, 3 * time.Millisecond})
}

func (s *RecoverySuite) TestRetryRecoveryUntilDoneStopsOnContextCancel(c *C) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts := 0
	// The supervisor waits BEFORE each attempt (startup just failed one), so a
	// canceled context means no attempt runs at all.
	recovered := retryRecoveryUntilDone(ctx, time.Hour, time.Hour, func() error {
		attempts++
		return nil
	}, func(error, int, time.Duration) {})

	c.Check(recovered, Equals, false)
	c.Check(attempts, Equals, 0)
}
