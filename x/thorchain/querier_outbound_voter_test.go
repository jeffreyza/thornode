package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

// Query selection is independent of quorum: even a pending destination voter
// must not hide the source's committed voter under the same migration tx ID.
func (s *QuerierSuite) TestQueryExplicitOutboundVoter(c *C) {
	for _, inboundCommitted := range []bool{false, true} {
		tx := GetRandomTx()
		tx.Chain = common.XMRChain
		tx.Memo = "MIGRATE:100"
		source, destination := GetRandomPubKey(), GetRandomPubKey()
		inTx := NewObservedTx(tx, 100, destination, 100)
		outTx := NewObservedTx(tx, 100, source, 100)
		inbound := NewObservedTxVoter(tx.ID, []common.ObservedTx{inTx})
		if inboundCommitted {
			inbound.Tx, inbound.Height = inTx, 110
		}
		outbound := NewObservedTxVoter(tx.ID, []common.ObservedTx{outTx})
		outbound.Tx, outbound.Height, outbound.FinalisedHeight = outTx, 109, 109
		s.k.SetObservedTxInVoter(s.ctx, inbound)
		s.k.SetObservedTxOutVoter(s.ctx, outbound)

		legacy, err := s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{TxId: tx.ID.String()})
		c.Assert(err, IsNil)
		c.Assert(legacy.Txs[0].ObservedPubKey, Equals, destination)
		for _, query := range []func(*types.QueryTxVotersRequest) (*types.QueryObservedTxVoter, error){
			func(req *types.QueryTxVotersRequest) (*types.QueryObservedTxVoter, error) {
				return s.queryServer.TxVoters(s.ctx, req)
			},
			func(req *types.QueryTxVotersRequest) (*types.QueryObservedTxVoter, error) {
				return s.queryServer.TxVotersOld(s.ctx, req)
			},
		} {
			selected, queryErr := query(&types.QueryTxVotersRequest{TxId: tx.ID.String(), Outbound: true})
			c.Assert(queryErr, IsNil)
			c.Assert(selected.Tx.ObservedPubKey, Equals, source)
			c.Assert(selected.Tx.Tx.ID, Equals, tx.ID)
			c.Assert(selected.Height, Equals, int64(109))
			c.Assert(selected.FinalisedHeight, Equals, int64(109))
		}
	}
}

func (s *QuerierSuite) TestQueryExplicitOutboundVoterNeverFallsBack(c *C) {
	tx := GetRandomTx()
	observed := NewObservedTx(tx, 100, GetRandomPubKey(), 100)
	voter := NewObservedTxVoter(tx.ID, []common.ObservedTx{observed})
	voter.Tx, voter.Height = observed, 110
	s.k.SetObservedTxInVoter(s.ctx, voter)
	missing, err := s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{TxId: tx.ID.String(), Outbound: true})
	c.Assert(err, NotNil)
	c.Assert(missing, IsNil)

	// Pending source observations are returned as pending, not replaced with
	// the destination's consensus. The client must still wait for source quorum.
	s.k.SetObservedTxOutVoter(s.ctx, NewObservedTxVoter(tx.ID, []common.ObservedTx{observed}))
	pending, err := s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{TxId: tx.ID.String(), Outbound: true})
	c.Assert(err, IsNil)
	c.Assert(pending.Height, Equals, int64(0))
	c.Assert(pending.Tx.Tx.IsEmpty(), Equals, true)

	// A reverted outbound is still returned as reverted, never substituted
	// with the otherwise usable inbound. The importing client rejects it.
	voter.Reverted = true
	s.k.SetObservedTxOutVoter(s.ctx, voter)
	selected, err := s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{TxId: tx.ID.String(), Outbound: true})
	c.Assert(err, IsNil)
	c.Assert(selected.Reverted, Equals, true)
}

func (s *QuerierSuite) TestQueryVoterDefaultOutboundFallback(c *C) {
	tx := GetRandomTx()
	observed := NewObservedTx(tx, 100, GetRandomPubKey(), 100)
	voter := NewObservedTxVoter(tx.ID, []common.ObservedTx{observed})
	voter.Tx, voter.Height = observed, 110
	s.k.SetObservedTxOutVoter(s.ctx, voter)
	selected, err := s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{TxId: tx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(selected.Tx.ObservedPubKey, Equals, observed.ObservedPubKey)
	for _, id := range []string{"", "invalid"} {
		_, err = s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{TxId: id, Outbound: true})
		c.Assert(err, NotNil)
	}
}
