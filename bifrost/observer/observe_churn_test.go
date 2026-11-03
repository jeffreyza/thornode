package observer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/common"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type churnRollbackClient struct {
	chainclients.ChainClient
	rollbacks int
}

func (c *churnRollbackClient) RollbackBlockScanner() error {
	c.rollbacks++
	return nil
}

func TestReadyToActiveRollsBackStartedScannersOnce(t *testing.T) {
	bridge := &MockThorchainBridge2{nodeStatus: stypes.NodeStatus_Ready}
	xmr, btc, unstarted := &churnRollbackClient{}, &churnRollbackClient{}, &churnRollbackClient{}
	o := &Observer{
		logger: zerolog.Nop(), thorchainBridge: bridge, lock: &sync.Mutex{},
		attestationGossip: &AttestationGossip{logger: zerolog.Nop(), host: NewMockHost(nil)},
		chains: map[common.Chain]chainclients.ChainClient{
			common.XMRChain: xmr, common.BTCChain: btc, common.ETHChain: unstarted,
		},
		startedChains: map[common.Chain]chainclients.ChainClient{common.XMRChain: xmr, common.BTCChain: btc},
	}
	t.Cleanup(o.attestationGossip.stopStartupStateSync)
	o.sendDeck(context.Background())
	require.Equal(t, stypes.NodeStatus_Ready, o.lastNodeStatus)
	require.Zero(t, xmr.rollbacks)
	bridge.nodeStatus = stypes.NodeStatus_Active
	o.sendDeck(context.Background())
	o.sendDeck(context.Background())
	collection := o.attestationGossip.startupState
	require.NotNil(t, collection)
	for range 3 {
		previousRound := collection.rounds
		collection.next = time.Now().Add(-time.Second)
		o.sendDeck(context.Background())
		require.Equal(t, previousRound+1, collection.rounds)
		require.Same(t, collection, o.attestationGossip.startupState)
	}
	require.Equal(t, stypes.NodeStatus_Active, o.lastNodeStatus)
	require.Equal(t, 1, xmr.rollbacks)
	require.Equal(t, 1, btc.rollbacks, "non-XMR churn rollback is unchanged")
	require.Zero(t, unstarted.rollbacks)
}
