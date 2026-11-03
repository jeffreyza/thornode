package config

import (
	"testing"

	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type ConfigSuite struct{}

var _ = Suite(&ConfigSuite{})

func (s *ConfigSuite) TestGet(c *C) {
	cfg := Get()
	c.Assert(len(cfg.StoragePath) > 0, Equals, true)
	c.Assert(len(cfg.Endpoints.Thornode) > 0, Equals, true)
	c.Assert(len(cfg.Endpoints.Midgard) > 0, Equals, true)
	c.Assert(len(cfg.Network) > 0, Equals, true)
	c.Assert(len(cfg.Links.Track) > 0, Equals, true)
	c.Assert(len(cfg.Links.Explorer) > 0, Equals, true)
	c.Assert(len(cfg.Links.Thornode) > 0, Equals, true)
	c.Assert(cfg.MaxRetries > 0, Equals, true)
	c.Assert(cfg.Endpoints.CacheSize > 0, Equals, true)
	c.Assert(cfg.TORAnchorCheckBlocks > int64(0), Equals, true)
	c.Assert(cfg.Thresholds.USDValue > uint64(0), Equals, true)
	c.Assert(cfg.Thresholds.RuneTransferValue > uint64(0), Equals, true)
	c.Assert(cfg.Thresholds.SwapDelta.USDValue > uint64(0), Equals, true)
	c.Assert(cfg.Thresholds.SwapDelta.BasisPoints > int64(0), Equals, true)
	c.Assert(cfg.Thresholds.Security.USDValue > uint64(0), Equals, true)
	c.Assert(cfg.Thresholds.SwapSlipBasisPoints > uint64(0), Equals, true)
	c.Assert(cfg.Thresholds.TORAnchorDriftBasisPoints > uint64(0), Equals, true)
	c.Assert(cfg.Styles.USDPerMoneyBag > uint64(0), Equals, true)
	c.Assert(len(cfg.LabeledAddresses) > 0, Equals, true)
	c.Assert(len(cfg.Notifications.Activity.Category) > 0, Equals, true)
	c.Assert(len(cfg.Notifications.Info.Category) > 0, Equals, true)
	c.Assert(len(cfg.Notifications.Lending.Category) > 0, Equals, true)
	c.Assert(len(cfg.Notifications.Security.Category) > 0, Equals, true)
	c.Assert(len(cfg.Notifications.Reschedules.Category) > 0, Equals, true)
	c.Assert(len(cfg.Notifications.FailedRefunds.Category) > 0, Equals, true)
}

func (s *ConfigSuite) TestHackBindEnv(c *C) {
	// hackBindEnv should not panic on a valid struct
	var cfg Config
	hackBindEnv(&cfg)
}
