//go:build mocknet
// +build mocknet

package wasmpermissions

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type WasmPermissionsSuite struct{}

var _ = Suite(&WasmPermissionsSuite{})

func (s *WasmPermissionsSuite) SetUpSuite(c *C) {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg.Seal()
}

func (s *WasmPermissionsSuite) TestCanStore(c *C) {
	perms := GetWasmPermissions()

	// Authorized address (true in mocknet map)
	addr, err := sdk.AccAddressFromBech32("tthor1jgnk2mg88m57csrmrlrd6c3qe4lag3e33y2f3k")
	c.Assert(err, IsNil)
	c.Assert(perms.CanStore(addr), IsNil)

	// Explicitly unauthorized address (false in mocknet map)
	addr2, err := sdk.AccAddressFromBech32("tthor1tdfqy34uptx207scymqsy4k5uzfmry5sf7z3dw")
	c.Assert(err, IsNil)
	c.Assert(perms.CanStore(addr2), NotNil)

	// Unknown address (not in map at all)
	addr3, err := sdk.AccAddressFromBech32("tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m")
	c.Assert(err, IsNil)
	c.Assert(perms.CanStore(addr3), NotNil)
}

func (s *WasmPermissionsSuite) TestCanInstantiate(c *C) {
	perms := GetWasmPermissions()

	// Authorized address
	addr, err := sdk.AccAddressFromBech32("tthor1jgnk2mg88m57csrmrlrd6c3qe4lag3e33y2f3k")
	c.Assert(err, IsNil)
	c.Assert(perms.CanInstantiate(addr), IsNil)

	// Explicitly unauthorized address
	addr2, err := sdk.AccAddressFromBech32("tthor1tdfqy34uptx207scymqsy4k5uzfmry5sf7z3dw")
	c.Assert(err, IsNil)
	c.Assert(perms.CanInstantiate(addr2), NotNil)

	// Unknown address
	addr3, err := sdk.AccAddressFromBech32("tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m")
	c.Assert(err, IsNil)
	c.Assert(perms.CanInstantiate(addr3), NotNil)
}

func (s *WasmPermissionsSuite) TestGetWasmPermissions(c *C) {
	perms := GetWasmPermissions()
	c.Assert(perms.Store, NotNil)
	c.Assert(perms.Instantiate, NotNil)
	c.Assert(len(perms.Store) > 0, Equals, true)
	c.Assert(len(perms.Instantiate) > 0, Equals, true)
}
