package monero

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestSignTxUnsupportedDestinationRetirement(t *testing.T) {
	for _, destination := range []string{
		"4CdvgtrwKjggnkXTAXRu7peYZruMJNii9EHrneXZie344btcwjvbzMbDvRcXvC4h4667vpwhdQWwPivq8ZfVPsGJeEvzNowpChm17H3MqL",
		common.XMRChecksummedDummyAddress('C'),
		nonGuaranteedFeaturedSubaddress,
	} {
		for _, state := range []string{"unsigned", "checkpoint", "reserved", "signed", "missing-store", "closed-store", "sidecar-not-configured"} {
			t.Run(destination[:8]+"/"+state, func(t *testing.T) {
				calls := 0
				frost := &mockFrostTssServer{fn: func(tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
					calls++
					return tssp.FrostKeySignResponse{}, errors.New("unexpected FROST signing")
				}}
				client, tx, server, _ := newFrostSignTxCleanupFixture(t, frost, 0)
				defer server.Close()
				var sidecarCalls atomic.Int32
				server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					sidecarCalls.Add(1)
					http.Error(w, "unexpected sidecar call", http.StatusBadRequest)
				})
				tx.ToAddress = common.Address(destination)
				identity, err := xmrSigningCacheIdentity(tx)
				if err != nil {
					t.Fatal(err)
				}
				var saved []byte
				switch state {
				case "checkpoint":
					tx.Checkpoint = []byte("saved-checkpoint")
				case "reserved", "signed":
					plan := testSignedTxClaimPlan(txCacheVault(tx, common.XMRChain), identity.CacheHash, identity.LogicalHash, txVaultPubKey(tx).String(), "msg", "plan", identity.ObserveKeys...)
					if err = client.signedTxPayloadStore.Reserve(plan); err != nil {
						t.Fatal(err)
					}
					if state == "signed" {
						saved, err = json.Marshal(signedTxPayload{TxHash: strings.Repeat("a", 64), TxMetadata: "aabb", TxKey: strings.Repeat("b", 64), MsgID: "msg"})
						if err != nil {
							t.Fatal(err)
						}
						if err = client.signedTxPayloadStore.Set(plan, strings.Repeat("a", 64), saved); err != nil {
							t.Fatal(err)
						}
					}
				case "missing-store":
					client.signedTxPayloadStore = nil
				case "closed-store":
					// Saved-payload recovery reads this store before the retirement
					// claim check, so this covers failure in that earlier read.
					if err = client.signedTxPayloadStore.db.Close(); err != nil {
						t.Fatal(err)
					}
				case "sidecar-not-configured":
					client.sidecar = nil
				}
				skip, skipErr := client.CanSkipUnsupportedXMRDestination(tx)
				if skip != (state == "unsigned") || (skip && skipErr != nil) {
					t.Fatalf("unsafe admission for %s: skip=%v err=%v", state, skip, skipErr)
				}
				signed, checkpoint, obs, signErr := client.SignTx(tx, 12345)
				if state == "signed" {
					if signErr != nil || !bytes.Equal(signed, saved) || len(checkpoint) == 0 || obs == nil {
						t.Fatalf("saved payload must recover before destination policy: %v", signErr)
					}
				} else {
					if signErr == nil || errors.Is(signErr, chainclienttypes.ErrUnsupportedXMRDestination) != (state == "unsigned") {
						t.Fatalf("unexpected disposition for %s: %v", state, signErr)
					}
					if len(signed) != 0 || len(checkpoint) != 0 || obs != nil {
						t.Fatal("unsupported request produced signing artifacts")
					}
				}
				bridge, ok := client.thorchainBridge.(*keysignFailureBridge)
				if !ok {
					t.Fatal("unexpected bridge fixture")
				}
				if calls != 0 || bridge.calls != 0 {
					t.Fatal("unsupported destination started signing or posted blame")
				}
				if sidecarCalls.Load() != 0 {
					t.Fatal("unsupported destination called sidecar")
				}
			})
		}
	}
}

func TestUnsupportedAdmissionPreservesIndexedRecoveryEvidence(t *testing.T) {
	client, tx, server, _ := newFrostSignTxCleanupFixture(t, &mockFrostTssServer{}, 0)
	defer server.Close()
	tx.ToAddress = common.Address(common.XMRChecksummedDummyAddress('C'))
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{
		signedTxPayloadKey(txCacheVault(tx, common.XMRChain), identity.CacheHash),
		signedTxPayloadClaimKey(identity.LogicalHash),
		signedTxCommittedLogicalKey(identity.LogicalHash),
		signedTxObservedLogicalKey(identity.LogicalHash),
		signedTxQuarantinedLogicalKey(identity.LogicalHash),
		signedTxBroadcastAttemptKey(identity.LogicalHash),
		signedTxPayloadObserveKey(identity.ObserveKeys[0]),
		signedTxCommittedAliasKey(identity.ObserveKeys[0]),
		signedTxQuarantinedAliasKey(identity.ObserveKeys[0]),
		signedTxObserveEpochKey(identity.ObserveKeys[0]),
	}
	for _, key := range keys {
		// Even malformed/partial evidence must remain available for recovery;
		// admission is not allowed to infer that an unreadable record is absent.
		if err = client.signedTxPayloadStore.db.Put([]byte(key), []byte("partial"), nil); err != nil {
			t.Fatal(err)
		}
		if skip, checkErr := client.CanSkipUnsupportedXMRDestination(tx); skip || checkErr != nil {
			t.Fatalf("discarded indexed recovery evidence %s: skip=%v err=%v", key, skip, checkErr)
		}
		if err = client.signedTxPayloadStore.db.Delete([]byte(key), nil); err != nil {
			t.Fatal(err)
		}
	}
	// The logical claim survives rescheduling to a replacement vault.
	if err = client.signedTxPayloadStore.db.Put([]byte(signedTxPayloadClaimKey(identity.LogicalHash)), []byte("old-vault-claim"), nil); err != nil {
		t.Fatal(err)
	}
	tx.VaultPubKeyFrost = common.PubKey("replacement-vault")
	if skip, checkErr := client.CanSkipUnsupportedXMRDestination(tx); skip || checkErr != nil {
		t.Fatalf("discarded prior vault claim: skip=%v err=%v", skip, checkErr)
	}
}

func TestUnsupportedAdmissionPreservesSignedCacheAlone(t *testing.T) {
	client, tx, server, _ := newFrostSignTxCleanupFixture(t, &mockFrostTssServer{}, 0)
	defer server.Close()
	tx.ToAddress = common.Address(common.XMRChecksummedDummyAddress('C'))
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatal(err)
	}
	// Establish that no other recovery index protects this fresh attempt.
	if skip, checkErr := client.CanSkipUnsupportedXMRDestination(tx); !skip || checkErr != nil {
		t.Fatalf("fresh unsupported attempt: skip=%v err=%v", skip, checkErr)
	}
	if err = client.signerCacheManager.SetSigned(identity.CacheHash, txCacheVault(tx, common.XMRChain), "recorded-transaction"); err != nil {
		t.Fatal(err)
	}
	if skip, checkErr := client.CanSkipUnsupportedXMRDestination(tx); skip || checkErr != nil {
		t.Fatalf("discarded signed-cache-only evidence: skip=%v err=%v", skip, checkErr)
	}
}
