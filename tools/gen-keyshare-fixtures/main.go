// Regenerate the keyshare test fixtures consumed by
// bifrost/tss/go-tss/keysign/ecdsa/keysign_test.go (4 files at
// bifrost/tss/go-tss/test_data/keysign_data/ecdsa/{0..3}.json).
//
// tss-lib v3 added LocalPartySaveData.ValidateWithProof, which requires
// PaillierSK.P and PaillierSK.Q to be populated. The v1-era committed
// fixtures don't have these (and can't be derived without factoring N),
// so a fresh 4-of-4 ECDSA keygen has to be run end-to-end.
//
// Run from repo root: go run ./tools/gen-keyshare-fixtures
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/test"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

// Same pubkeys as bifrost/tss/go-tss/keysign/ecdsa/keysign_test.go.
var testPubKeys = []string{
	"thorpub1addwnpepq2ryyje5zr09lq7gqptjwnxqsy2vcdngvwd6z7yt5yjcnyj8c8cn559xe69",
	"thorpub1addwnpepqfjcw5l4ay5t00c32mmlky7qrppepxzdlkcwfs2fd5u73qrwna0vzag3y4j",
	"thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3",
	"thorpub1addwnpepqtspqyy6gk22u37ztra4hq3hdakc0w0k60sfy849mlml2vrpfr0wvm6uz09",
}

const (
	outDir    = "bifrost/tss/go-tss/test_data/keysign_data/ecdsa"
	threshold = 1 // 2-of-4
)

func main() {
	conversion.SetupBech32Prefix()

	// Sort the pubkeys to match GetParties' contract; the file layout in the
	// keysign test maps file index i to the unsorted testPubKeys[i], but the
	// keygen protocol requires the sorted order — so we keep both.
	sortedPubKeys := append([]string(nil), testPubKeys...)
	sort.Strings(sortedPubKeys)

	partiesID, _, err := conversion.GetParties(sortedPubKeys, sortedPubKeys[0])
	if err != nil {
		die("GetParties: %v", err)
	}
	n := len(partiesID)

	// Precompute preParams in parallel so the keygen rounds aren't blocked on
	// prime generation. Each call takes ~5–15s on a modern machine.
	fmt.Fprintln(os.Stderr, "Generating preParams...")
	preParamsCh := make(chan struct {
		idx int
		p   *keygen.LocalPreParams
	}, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			start := time.Now()
			pp, ppErr := keygen.GeneratePreParams(20 * time.Minute)
			if ppErr != nil {
				die("preParams[%d]: %v", idx, ppErr)
			}
			fmt.Fprintf(os.Stderr, "  party %d preParams in %s\n", idx, time.Since(start))
			preParamsCh <- struct {
				idx int
				p   *keygen.LocalPreParams
			}{idx, pp}
		}(i)
	}
	preParams := make([]*keygen.LocalPreParams, n)
	for i := 0; i < n; i++ {
		r := <-preParamsCh
		preParams[r.idx] = r.p
	}

	// Set up the in-process keygen protocol.
	p2pCtx := tss.NewPeerContext(partiesID)
	parties := make([]*keygen.LocalParty, 0, n)
	errCh := make(chan *tss.Error, n)
	outCh := make(chan tss.Message, n*10)
	endCh := make(chan *keygen.LocalPartySaveData, n)
	updater := test.SharedPartyUpdater

	for i := 0; i < n; i++ {
		params := tss.NewParameters(tss.S256(), p2pCtx, partiesID[i], n, threshold)
		party, ok := keygen.NewLocalParty(params, outCh, endCh, *preParams[i]).(*keygen.LocalParty)
		if !ok {
			die("party %d: NewLocalParty did not return *LocalParty", i)
		}
		parties = append(parties, party)
		go func(P *keygen.LocalParty) {
			if startErr := P.Start(); startErr != nil {
				errCh <- startErr
			}
		}(party)
	}

	fmt.Fprintln(os.Stderr, "Running keygen protocol...")
	saveData := make([]*keygen.LocalPartySaveData, n)
	var done int32
loop:
	for {
		select {
		case err := <-errCh:
			die("keygen: %v", err)
		case msg := <-outCh:
			dest := msg.GetTo()
			if dest == nil {
				// Broadcast.
				for _, P := range parties {
					if P.PartyID().Index == msg.GetFrom().Index {
						continue
					}
					go updater(P, msg, errCh)
				}
			} else {
				if dest[0].Index == msg.GetFrom().Index {
					die("party %d tried to send to itself", dest[0].Index)
				}
				go updater(parties[dest[0].Index], msg, errCh)
			}
		case save := <-endCh:
			idx, idxErr := save.OriginalIndex()
			if idxErr != nil {
				die("OriginalIndex: %v", idxErr)
			}
			saveData[idx] = save
			if atomic.AddInt32(&done, 1) == int32(n) {
				break loop
			}
		}
	}

	// Sanity: every save must validate under the new v3 rules.
	for i, sd := range saveData {
		if !sd.ValidateWithProof() {
			die("party %d save data failed ValidateWithProof", i)
		}
	}

	vaultPubKey, _, err := conversion.GetTssPubKeyECDSA(saveData[0].ECDSAPub)
	if err != nil {
		die("GetTssPubKeyECDSA: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Vault pubkey: %s\n", vaultPubKey)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		die("mkdir: %v", err)
	}

	// saveData[k] belongs to partiesID[k], where partiesID is sorted by
	// PartyID.KeyInt (the big.Int of the compressed pubkey bytes) — see
	// btss.SortPartyIDs. That order is NOT the same as the lexicographic
	// order of the bech32-encoded pubkey strings; for the four test keys,
	// lex idx 0 ↔ KeyInt idx 1.
	//
	// The keysign test names its libp2p comms by the unsorted (== lex)
	// position of testPubKeys, so file `i.json` must contain the keyshare
	// for sortedPubKeys[i]. `conversion.GetParties` sets each PartyID's
	// `Id` field to the pre-sort lex index as a string, so we use that to
	// route saveData[k] (KeyInt order) into file lex-idx.json.
	for k := 0; k < n; k++ {
		lexIdx, err := strconv.Atoi(partiesID[k].Id)
		if err != nil {
			die("partiesID[%d].Id %q is not an int: %v", k, partiesID[k].Id, err)
		}
		localBytes, err := json.Marshal(saveData[k])
		if err != nil {
			die("marshal local data %d: %v", k, err)
		}
		state := storage.KeygenLocalState{
			PubKey:          vaultPubKey,
			LocalData:       localBytes,
			ParticipantKeys: sortedPubKeys,
			LocalPartyKey:   sortedPubKeys[lexIdx],
		}
		wrap, err := json.Marshal(state)
		if err != nil {
			die("marshal wrapper %d: %v", k, err)
		}
		path := fmt.Sprintf("%s/%d.json", outDir, lexIdx)
		if err := os.WriteFile(path, wrap, 0o600); err != nil {
			die("write %s: %v", path, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (party=%s, keyInt-idx=%d)\n", path, sortedPubKeys[lexIdx], k)
	}

	// A fresh regen randomises the vault pubkey. After running this, update
	// the `PoolPubKey` literal in the three `keysign.NewRequest` calls in
	// bifrost/tss/go-tss/keysign/ecdsa/keysign_test.go to match.
	fmt.Println(vaultPubKey)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
