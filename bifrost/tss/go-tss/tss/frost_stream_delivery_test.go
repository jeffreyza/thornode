package tss

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

// Exercise real restricted subscriptions. The transport must close a delivered
// stream even when the coordinator has not consumed its queued frame yet.
func newRestrictedFrostTestReceiver(t *testing.T) func(*testing.T, messages.THORChainTSSMessageType, ...[]byte) *p2p.Message {
	ensureBech32Prefix()
	start := func(index int) *p2p.Communication {
		encoded, err := base64.StdEncoding.DecodeString(testPriKeyArr[index])
		if err != nil {
			t.Fatal(err)
		}
		key, err := hex.DecodeString(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		comm, err := p2p.NewCommunication(&p2p.Config{Port: 0, RendezvousString: "frost-inbox-delivery"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := comm.Start(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if stopErr := comm.Stop(); stopErr != nil {
				t.Errorf("stop restricted receiver: %v", stopErr)
			}
		})
		return comm
	}
	receiver, sender := start(0), start(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := sender.GetHost().Connect(ctx, peer.AddrInfo{ID: receiver.GetHost().ID(), Addrs: receiver.GetHost().Addrs()}); err != nil {
		t.Fatal(err)
	}
	receive := func(t *testing.T, topic messages.THORChainTSSMessageType, bodies ...[]byte) *p2p.Message {
		t.Helper()
		channel := make(chan *p2p.Message, 1)
		id := t.Name()
		receiver.SetSubscribeWithPeers(topic, id, channel, []peer.ID{sender.GetHost().ID()}, 4096, 1)
		t.Cleanup(func() { receiver.CancelSubscribe(topic, id) })
		body := []byte("{}")
		if len(bodies) > 0 {
			body = bodies[0]
		}
		payload, err := json.Marshal(messages.WrappedMessage{MessageType: topic, MsgID: id, Payload: body})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := sender.GetHost().NewStream(ctx, receiver.GetHost().ID(), p2p.TSSProtocolID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		if err := p2p.WriteStreamWithBuffer(payload, stream); err != nil {
			t.Fatal(err)
		}
		if err := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var ack [1]byte
		if n, err := stream.Read(ack[:]); n != 0 || err != io.EOF {
			t.Fatalf("handler did not close the queued frame's stream: n=%d err=%v", n, err)
		}
		select {
		case msg := <-channel:
			return msg
		case <-ctx.Done():
			t.Fatal("restricted frame was not delivered")
			return nil
		}
	}
	return receive
}

func TestFrostKeysignResultWaiterDrainsNonResultQueues(t *testing.T) {
	receive := newRestrictedFrostTestReceiver(t)
	for _, topic := range []messages.THORChainTSSMessageType{
		messages.TSSFrostPreprocessMsg, messages.TSSFrostShareMsg, messages.TSSFrostVerifyMsg,
	} {
		t.Run(topic.String(), func(t *testing.T) {
			msg := receive(t, topic)
			inbox := newFrostSignInbox()
			channel, ok := inbox.messageChannel(topic)
			if !ok {
				t.Fatal("missing inbox channel")
			}
			channel <- msg
			coord := newTestFrostSignCoordinator(testPubKeys[0], "different-session", inbox, time.Second)
			if _, err := coord.WaitForResult([]string{testPubKeys[0]}, 1, 100*time.Millisecond); err == nil {
				t.Fatal("result waiter unexpectedly succeeded without a result")
			}
			if len(channel) != 0 {
				t.Fatal("keysign result waiter left non-result traffic queued")
			}
		})
	}
}

func TestFrostInboxStreamsCloseBeforeConsumption(t *testing.T) {
	receive := newRestrictedFrostTestReceiver(t)
	t.Run("keygen verification discard", func(t *testing.T) {
		msg := receive(t, messages.TSSFrostVerifyMsg)
		coord := newTestFrostKeygenCoordinator(testPubKeys[0], "different-session", newFrostKeygenInbox(), time.Second)
		coord.routeVerificationMessage(msg, "round1")
	})
	for _, topic := range []messages.THORChainTSSMessageType{
		messages.TSSFrostDKGRound1Msg, messages.TSSFrostDKGRound2Msg,
		messages.TSSFrostDKGRound3Msg, messages.TSSFrostVerifyMsg,
	} {
		t.Run("result waiter drains "+topic.String(), func(t *testing.T) {
			msg := receive(t, topic)
			inbox := newFrostKeygenInbox()
			switch topic {
			case messages.TSSFrostDKGRound1Msg:
				inbox.round1 <- msg
			case messages.TSSFrostDKGRound2Msg:
				inbox.round2 <- msg
			case messages.TSSFrostDKGRound3Msg:
				inbox.round3 <- msg
			case messages.TSSFrostVerifyMsg:
				inbox.verify <- msg
			}
			coord := newTestFrostKeygenCoordinator(testPubKeys[0], "different-session", inbox, time.Second)
			if _, err := coord.WaitForResult([]string{testPubKeys[0]}, 1, 100*time.Millisecond); err == nil {
				t.Fatal("result waiter unexpectedly succeeded without a result")
			}
			if len(inbox.round1)+len(inbox.round2)+len(inbox.round3)+len(inbox.verify) != 0 {
				t.Fatal("result waiter left non-result traffic queued")
			}
		})
	}
}
