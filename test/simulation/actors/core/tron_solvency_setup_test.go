package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cosmos/go-bip39"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestTRONSolvencySetupRequiresAtLeastFourMatchingActiveValidators(t *testing.T) {
	for _, test := range []struct {
		name       string
		active     int
		nodeUsers  int
		totalNodes int
	}{
		{name: "missing churn", active: 1, nodeUsers: 4, totalNodes: 4},
		{name: "no active validators", active: 0, nodeUsers: 4, totalNodes: 4},
		{name: "incomplete activation", active: 3, nodeUsers: 4, totalNodes: 4},
		{name: "single node harness", active: 1, nodeUsers: 1, totalNodes: 1},
		{name: "missing simulation user", active: 4, nodeUsers: 3, totalNodes: 4},
		{name: "extra active validator", active: 5, nodeUsers: 4, totalNodes: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			nodes := make([]openapi.Node, test.totalNodes)
			for i := range nodes {
				nodes[i] = openapi.Node{
					NodeAddress: fmt.Sprintf("validator-%d", i),
					Status:      atypes.NodeStatus_Standby.String(),
				}
				if i < test.active {
					nodes[i].Status = atypes.NodeStatus_Active.String()
				}
			}
			// Nil clients prove the count check happens before any client use.
			config := &OpConfig{NodeUsers: make([]*User, test.nodeUsers)}
			users, addresses, err := activeTRONSolvencyUsers(config, nodes)
			if err == nil {
				t.Fatal("invalid setup must return an error, not wait for activation")
			}
			for _, want := range []string{
				"requires at least 4 active validators",
				fmt.Sprintf("got %d active validators and %d simulation users", test.active, test.nodeUsers),
				"make test-simulation-cluster TEST_SIMULATION_CLUSTER_STAGES=seed,bootstrap,churn,tron-solvency-fragmentation",
				"churn stage before tron-solvency-fragmentation",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("setup error %q is missing %q", err, want)
				}
			}
			if users != nil || addresses != nil {
				t.Fatal("invalid setup must not return users for broadcasting")
			}
		})
	}
}

func TestTRONSolvencySetupAcceptsAlreadyActivatedCluster(t *testing.T) {
	for _, count := range []int{4, 5} {
		t.Run(fmt.Sprintf("%d validators", count), func(t *testing.T) {
			config, nodes := tronSolvencySetupFixture(t, count)
			// API ordering and extra standby nodes must not affect active membership.
			nodes[0], nodes[3] = nodes[3], nodes[0]
			nodes = append(nodes, openapi.Node{NodeAddress: "standby", Status: atypes.NodeStatus_Standby.String()})

			users, addresses, err := activeTRONSolvencyUsers(config, nodes)
			if err != nil {
				t.Fatalf("already activated cluster must not require another churn: %v", err)
			}
			if len(users) != count || len(addresses) != count {
				t.Fatalf("expected %d matched users, got users=%d addresses=%d", count, len(users), len(addresses))
			}
			for i, user := range users {
				want, err := config.NodeUsers[i].PubKey(common.THORChain).GetThorAddress()
				if err != nil {
					t.Fatal(err)
				}
				if user != config.NodeUsers[i] || addresses[i] != want.String() {
					t.Fatalf("wrong user/address pairing at index %d", i)
				}
			}
		})
	}
}

func TestTRONSolvencySetupRejectsUnmatchedActiveMembership(t *testing.T) {
	config, nodes := tronSolvencySetupFixture(t, 4)
	// Four active accounts are insufficient if one is not a simulation signer.
	nodes[0].NodeAddress = "different-validator"
	users, addresses, err := activeTRONSolvencyUsers(config, nodes)
	if err == nil || !strings.Contains(err.Error(), "missing simulation users for active validators") {
		t.Fatalf("expected membership setup error, got %v", err)
	}
	if users != nil || addresses != nil {
		t.Fatal("unmatched membership must not return users for broadcasting")
	}
}

func TestTRONSolvencySetupSelectsActiveUsersWithStandbyClient(t *testing.T) {
	for standbyIndex := range 5 {
		t.Run(fmt.Sprintf("standby client %d", standbyIndex), func(t *testing.T) {
			config, nodes := tronSolvencySetupFixture(t, 5)
			nodes[standbyIndex].Status = atypes.NodeStatus_Standby.String()
			standbyAddress := nodes[standbyIndex].NodeAddress
			// Membership must come from addresses, not API ordering or a fixed
			// assumption about which configured client remains on standby.
			nodes[0], nodes[4] = nodes[4], nodes[0]
			nodes[1], nodes[3] = nodes[3], nodes[1]
			users, addresses, err := activeTRONSolvencyUsers(config, nodes)
			if err != nil {
				t.Fatalf("four active validators and a standby client must be accepted: %v", err)
			}
			if len(users) != 4 || len(addresses) != 4 {
				t.Fatalf("expected four active clients, got users=%d addresses=%d", len(users), len(addresses))
			}
			selected := 0
			for i, user := range config.NodeUsers {
				if i == standbyIndex {
					continue
				}
				address, addressErr := user.PubKey(common.THORChain).GetThorAddress()
				if addressErr != nil {
					t.Fatal(addressErr)
				}
				if users[selected] != user || addresses[selected] != address.String() {
					t.Fatalf("active client order or address pairing changed at index %d", selected)
				}
				selected++
			}

			// A busy standby client must not prevent this actor from locking
			// the active set, or be included in its slash-point snapshots.
			standby := config.NodeUsers[standbyIndex]
			if !standby.Acquire() {
				t.Fatal("failed to acquire standby client")
			}
			t.Cleanup(standby.Release)
			release, acquired := acquireTRONSolvencyUsers(users)
			if !acquired {
				t.Fatal("standby client lock must not block the active clients")
			}
			t.Cleanup(release)
			points, err := slashPointsForAddresses(nodes, addresses)
			if err != nil {
				t.Fatal(err)
			}
			if _, found := points[standbyAddress]; found {
				t.Fatal("standby client must not appear in slash-point snapshots")
			}
		})
	}
}

func TestTRONSolvencySetupRejectsDuplicateActiveClient(t *testing.T) {
	for _, replaceMissingClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace missing client %t", replaceMissingClient), func(t *testing.T) {
			config, nodes := tronSolvencySetupFixture(t, 4)
			if replaceMissingClient {
				config.NodeUsers[3] = config.NodeUsers[0]
			} else {
				config.NodeUsers = append(config.NodeUsers, config.NodeUsers[0])
			}
			users, addresses, err := activeTRONSolvencyUsers(config, nodes)
			if err == nil || !strings.Contains(err.Error(), "duplicate simulation user for active validator") {
				t.Fatalf("expected duplicate active client error, got %v", err)
			}
			if users != nil || addresses != nil {
				t.Fatal("duplicate clients must not be returned for broadcasting")
			}
		})
	}
}

func tronSolvencySetupFixture(t *testing.T, validatorCount int) (*OpConfig, []openapi.Node) {
	t.Helper()
	config := &OpConfig{}
	nodes := make([]openapi.Node, validatorCount)
	for i := range nodes {
		// Deterministic test-only keys and in-memory clients; no RPC calls.
		entropy := make([]byte, 32)
		entropy[0] = byte(i + 1)
		mnemonic, err := bip39.NewMnemonic(entropy)
		if err != nil {
			t.Fatal(err)
		}
		user := NewUser(mnemonic, nil)
		address, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			t.Fatal(err)
		}
		config.NodeUsers = append(config.NodeUsers, user)
		nodes[i] = openapi.Node{NodeAddress: address.String(), Status: atypes.NodeStatus_Active.String()}
	}
	return config, nodes
}
