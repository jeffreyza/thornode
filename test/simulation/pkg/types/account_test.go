package types

import (
	"errors"
	"os"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

func AccountTest(t *testing.T) { TestingT(t) }

type AccountSuite struct {
	chainID    string
	hadChainID bool
}

var _ = Suite(&AccountSuite{})

func (s *AccountSuite) SetUpTest(c *C) {
	s.chainID, s.hadChainID = os.LookupEnv("CHAIN_ID")
	c.Assert(os.Setenv("CHAIN_ID", "thorchain-mocknet-v1"), IsNil)
	config.Init()
}

func (s *AccountSuite) TearDownTest(c *C) {
	if s.hadChainID {
		c.Assert(os.Setenv("CHAIN_ID", s.chainID), IsNil)
	} else {
		c.Assert(os.Unsetenv("CHAIN_ID"), IsNil)
	}
	config.Init()
}

func (s *AccountSuite) TestNewUser(c *C) {
	testMnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
	constructors := make(map[common.Chain]LiteChainClientConstructor)
	user := NewUser(testMnemonic, constructors)

	c.Assert(user, NotNil)
}

func TestNewChainClientWithRetryRetriesContextDeadlineExceeded(t *testing.T) {
	client := &testLiteChainClient{}
	attempts := 0
	sleepCalls := 0

	got, err := newChainClientWithRetryAndSleep(
		common.DOGEChain,
		func(common.Chain, *thorclient.Keys) (LiteChainClient, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New(`fail to import address(mhCAVg92btMvxr2f3sf1K2hTthpgZEMuZo): Post "http://localhost:18332": context deadline exceeded`)
			}
			return client, nil
		},
		nil,
		func(time.Duration) {
			sleepCalls++
		},
	)
	if err != nil {
		t.Fatalf("expected transient startup error to recover, got %v", err)
	}
	if got != client {
		t.Fatal("expected recovered client to be returned")
	}
	if attempts != 3 {
		t.Fatalf("expected 3 constructor attempts, got %d", attempts)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestNewChainClientWithRetryStopsOnPermanentError(t *testing.T) {
	attempts := 0
	sleepCalls := 0
	wantErr := errors.New("unsupported chain")

	got, err := newChainClientWithRetryAndSleep(
		common.DOGEChain,
		func(common.Chain, *thorclient.Keys) (LiteChainClient, error) {
			attempts++
			return nil, wantErr
		},
		nil,
		func(time.Duration) {
			sleepCalls++
		},
	)
	if err != wantErr {
		t.Fatalf("expected permanent error %v, got %v", wantErr, err)
	}
	if got != nil {
		t.Fatal("expected no client on permanent error")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 constructor attempt, got %d", attempts)
	}
	if sleepCalls != 0 {
		t.Fatalf("expected no retry sleeps, got %d", sleepCalls)
	}
}

func TestNewChainClientWithRetryRetriesWalletLoadRace(t *testing.T) {
	client := &testLiteChainClient{}
	attempts := 0
	sleepCalls := 0

	got, err := newChainClientWithRetryAndSleep(
		common.LTCChain,
		func(common.Chain, *thorclient.Keys) (LiteChainClient, error) {
			attempts++
			if attempts < 4 {
				return nil, errors.New(`fail to query address info(rltc1quuds8pd92qnnq0udw0rpg0szpgcslc9p9h3t2p): -18: No wallet is loaded. Load a wallet using loadwallet or create a new one with createwallet. (Note: A default wallet is no longer automatically created)`)
			}
			return client, nil
		},
		nil,
		func(time.Duration) {
			sleepCalls++
		},
	)
	if err != nil {
		t.Fatalf("expected LTC wallet load race to recover, got %v", err)
	}
	if got != client {
		t.Fatal("expected recovered client to be returned")
	}
	if attempts != 4 {
		t.Fatalf("expected 4 constructor attempts, got %d", attempts)
	}
	if sleepCalls != 3 {
		t.Fatalf("expected 3 retry sleeps, got %d", sleepCalls)
	}
}

type testLiteChainClient struct{}

func (t *testLiteChainClient) GetAccount(_ *common.PubKey) (*common.Account, error) {
	return nil, nil
}

func (t *testLiteChainClient) SignTx(SimTx) ([]byte, error) {
	return nil, nil
}

func (t *testLiteChainClient) BroadcastTx([]byte) (string, error) {
	return "", nil
}
