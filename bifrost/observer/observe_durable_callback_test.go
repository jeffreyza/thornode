package observer

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

type durableObserverTestClient struct {
	mockChainClient
	err    error
	calls  int
	during func(types.TxInItem)
}

func (c *durableObserverTestClient) GetChain() common.Chain { return common.XMRChain }

func (c *durableObserverTestClient) OnObservedTxInDurable(item types.TxInItem, _ int64) error {
	c.calls++
	if c.during != nil {
		c.during(item)
	}
	return c.err
}

func TestObserverWaitsForDurableChainCallbackBeforeDeckMutation(t *testing.T) {
	storage, err := NewObserverStorage(filepath.Join(t.TempDir(), "observer"), config.LevelDBOptions{})
	if err != nil {
		t.Fatalf("open observer storage: %v", err)
	}
	defer storage.Close()

	client := &durableObserverTestClient{}
	observer := &Observer{
		logger:  zerolog.Nop(),
		chains:  map[common.Chain]chainclients.ChainClient{common.XMRChain: client},
		onDeck:  make(map[txInKey]*types.TxIn),
		lock:    &sync.Mutex{},
		storage: storage,
	}
	txID := common.TxID("3d8ad5e2a238d4a64e3a9b8994bdbee22cdd2af8dcf4484dfaadd5cc4f19be2d")
	inboundID := "0b12ab7c106c00572e5a5d4905bfa693f07248bb035bbc28b688cec23fb52122"
	coins := common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(25)))
	deck := &types.TxIn{
		Chain:                common.XMRChain,
		ConfirmationRequired: 10,
		TxArray: []*types.TxInItem{{
			BlockHeight:         100,
			Tx:                  txID.String(),
			Memo:                "OUT:" + inboundID,
			Sender:              "vault-source",
			To:                  "recipient",
			Coins:               coins,
			ObservedVaultPubKey: common.EmptyPubKey,
		}},
	}
	key := TxInKey(deck)
	observer.onDeck[key] = deck
	if err = storage.AddOrUpdateTx(deck); err != nil {
		t.Fatalf("persist observer deck: %v", err)
	}
	committed := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: common.Address("vault-source"),
			ToAddress:   common.Address("recipient"),
			Coins:       coins,
			Memo:        "OUT:" + inboundID,
		},
		BlockHeight:    100,
		XmrMinedHeight: 100,
		FinaliseHeight: 110,
		ObservedPubKey: common.EmptyPubKey,
	}

	client.err = errors.New("claim db unavailable")
	observer.handleObservedTxCommitted(committed, false)
	if observer.onDeck[key].TxArray[0].CommittedUnFinalised {
		t.Fatal("observer marked item committed after durable callback failure")
	}
	persisted, err := storage.GetOnDeckTxs()
	if err != nil || len(persisted) != 1 || persisted[0].TxArray[0].CommittedUnFinalised {
		t.Fatalf("durable callback failure mutated stored deck: deck=%#v err=%v", persisted, err)
	}

	client.err = nil
	observer.handleObservedTxCommitted(committed, false)
	if !observer.onDeck[key].TxArray[0].CommittedUnFinalised {
		t.Fatal("observer did not persist committed-unfinalized state after callback success")
	}

	final := committed
	final.BlockHeight = final.FinaliseHeight
	client.err = errors.New("final tombstone unavailable")
	client.during = func(item types.TxInItem) {
		if item.CommittedUnFinalised {
			t.Error("final callback retained committed-unfinalized flag")
		}
		if _, ok := observer.onDeck[key]; !ok {
			t.Error("observer deleted deck before durable final callback")
		}
	}
	observer.handleObservedTxCommitted(final, false)
	if _, ok := observer.onDeck[key]; !ok {
		t.Fatal("observer deleted deck after durable final callback failure")
	}

	client.err = nil
	observer.handleObservedTxCommitted(final, false)
	if _, ok := observer.onDeck[key]; ok {
		t.Fatal("observer retained deck after durable final callback and storage deletion succeeded")
	}
	persisted, err = storage.GetOnDeckTxs()
	if err != nil || len(persisted) != 0 {
		t.Fatalf("finalized deck remained in storage: deck=%#v err=%v", persisted, err)
	}
}
