//go:build mainnet
// +build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"
)

// TestSlashRefund19to20SumMatchesTotal pins the slash-refund data: the 218
// per-bond-provider refunds must sum exactly to the reversed slash total.
func (s *Migration19to20Suite) TestSlashRefund19to20SumMatchesTotal(c *C) {
	var sum uint64
	for _, r := range mainnetSlashRefunds19to20 {
		sum += r.amount
	}
	c.Assert(len(mainnetSlashRefunds19to20), Equals, 218)
	c.Assert(sum, Equals, mainnetSlashRefunds19to20Total)
	c.Assert(sum, Equals, uint64(11354727112439)) // 113,547.27112439 RUNE
}
