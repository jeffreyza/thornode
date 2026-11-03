package core

import (
	"encoding/json"
	rand "math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// These are the existing structured log messages consumed by the real actor.
const recoveryLogFixture = `{"message":"TEST override active: XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS shrinks the round-3 wait","msg_id":"ceremony","round3_timeout":100}
{"message":"TEST injected XMR FROST round-3 timeout","msg_id":"ceremony","round":"frost_dkg_round3"}
{"message":"local frost keygen failed but quorum succeeded; adopted result with locally finalized keyshare","msg_id":"ceremony","address":"xmr-address"}
`

func TestXMRKeygenRecoveryRequiresFaultEvidence(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(recoveryLogFixture), "\n")
	for _, tc := range []struct {
		name, log string
		want      int
	}{
		{"same ceremony", recoveryLogFixture, 1},
		{"ordinary success", lines[0], 0},
		{"missing override", strings.Join(lines[1:], "\n"), 0},
		{"missing timeout", lines[0] + "\n" + lines[2], 0},
		{"missing adoption", strings.Join(lines[:2], "\n"), 0},
		{"wrong round", strings.ReplaceAll(recoveryLogFixture, "frost_dkg_round3", "frost_dkg_round2"), 0},
		{"different ceremony", lines[0] + "\n" + lines[1] + "\n" + strings.ReplaceAll(lines[2], "ceremony", "other"), 0},
		{"empty ceremony", strings.ReplaceAll(recoveryLogFixture, "ceremony", ""), 0},
		{"disabled override", strings.ReplaceAll(recoveryLogFixture, ":100", ":0"), 0},
		{"empty address", strings.ReplaceAll(recoveryLogFixture, "xmr-address", ""), 0},
		{"wrong order", lines[2] + "\n" + lines[1] + "\n" + lines[0], 0},
		{"truncated adoption", lines[0] + "\n" + lines[1] + "\n" + lines[2][:40], 0},
		{"unrelated and truncated lines", "not json\n" + recoveryLogFixture + "{\"message\":", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pig.log")
			if err := os.WriteFile(path, []byte(tc.log), 0o600); err != nil {
				t.Fatal(err)
			}
			a := &XMRKeygenRecoveryActor{evidenceLog: path}
			got, err := a.readRecoveryEvidence()
			if err != nil || len(got) != tc.want {
				t.Fatalf("evidence=%+v error=%v", got, err)
			}
			if tc.want == 1 && (got[0].Ceremony != "ceremony" || got[0].Address != "xmr-address") {
				t.Fatalf("wrong recovery: %+v", got)
			}
		})
	}
}

func TestXMRKeygenRecoveryRequiresLogAtEntry(t *testing.T) {
	t.Setenv("XMR_KEYGEN_RECOVERY_LOG", "")
	a := NewXMRKeygenRecoveryActor(rand.New(rand.NewPCG(1, 2)))
	if len(a.Ops) != 3 || a.Timeout != 20*time.Minute {
		t.Fatal("recovery actor must capture, churn and verify within 20 minutes")
	}
	result := a.Ops[0](nil)
	if result.Error == nil || !result.Finish || result.Continue {
		t.Fatalf("missing evidence source must fail before churn: %+v", result)
	}
	t.Setenv("XMR_KEYGEN_RECOVERY_LOG", filepath.Join(t.TempDir(), "missing"))
	a = NewXMRKeygenRecoveryActor(rand.New(rand.NewPCG(1, 2)))
	if result = a.Ops[0](nil); result.Error == nil || !result.Finish {
		t.Fatalf("unreadable evidence source must fail: %+v", result)
	}
}

func recoveryFixture(t *testing.T) (*XMRKeygenRecoveryActor, []openapi.Vault, []openapi.Node, []xmrRecoveryMetric, []xmrRecoveryEvidence) {
	t.Helper()
	key := "new"
	v := openapi.Vault{PubKey: &key, PubKeyFrost: &key, Status: "ActiveVault", Membership: []string{"one", "two", "three", "four"}, Addresses: []openapi.VaultAddress{{Chain: "XMR", Address: "xmr-address"}}}
	nodes := make([]openapi.Node, 4)
	for i, member := range v.Membership {
		nodes[i].Status = "Active"
		nodes[i].NodeAddress = member
		nodes[i].PubKeySet.SetSecp256k1(member)
	}
	var metrics []xmrRecoveryMetric
	if err := json.Unmarshal([]byte(`[{"pub_key":"new","node_tss_times":[{"address":"one","tss_time":100},{"address":"two","tss_time":101},{"address":"three","tss_time":102},{"address":"four","tss_time":"103"}]}]`), &metrics); err != nil {
		t.Fatal(err)
	}
	a := &XMRKeygenRecoveryActor{Actor: *NewActor("recovery-test", rand.New(rand.NewPCG(1, 2))), vaultsBefore: map[string]bool{}}
	return a, []openapi.Vault{v}, nodes, metrics, parseXMRRecoveryEvidence([]byte(recoveryLogFixture))
}

func TestXMRKeygenRecoveryRejectsFalsePasses(t *testing.T) {
	for _, name := range []string{"recovered", "old vault", "inactive vault", "wrong member", "duplicate member", "missing node", "wrong active node", "missing XMR address", "wrong address", "wrong chain", "no frost key", "no fault", "wrong metric", "missing vote", "duplicate vote", "outsider vote", "zero timing", "missing timing", "jail before vault", "jail with vault"} {
		t.Run(name, func(t *testing.T) {
			a, v, n, m, e := recoveryFixture(t)
			wantError := strings.HasPrefix(name, "jail")
			switch name {
			case "old vault":
				a.vaultsBefore["new"] = true
			case "inactive vault":
				v[0].Status = "RetiringVault"
			case "wrong member":
				v[0].Membership[3] = "outsider"
			case "duplicate member":
				v[0].Membership[3] = "one"
			case "missing node":
				n = n[:3]
			case "wrong active node":
				n[3].PubKeySet.SetSecp256k1("outsider")
			case "missing XMR address":
				v[0].Addresses = nil
			case "wrong address":
				v[0].Addresses[0].Address = "other-address"
			case "wrong chain":
				v[0].Addresses[0].Chain = "BTC"
			case "no frost key":
				v[0].PubKeyFrost = nil
			case "no fault":
				e = nil
			case "wrong metric":
				m[0].PubKey = "other-vault"
			case "missing vote":
				m[0].NodeTssTimes = m[0].NodeTssTimes[:3]
			case "duplicate vote":
				m[0].NodeTssTimes[3].Address = "one"
			case "outsider vote":
				m[0].NodeTssTimes[3].Address = "outsider"
			case "zero timing":
				m[0].NodeTssTimes[3].TssTime = "0"
			case "missing timing":
				m[0].NodeTssTimes[3].TssTime = ""
			case "jail before vault":
				v = nil
				n[0].Jail.SetReason(xmrKeygenJailReason)
			case "jail with vault":
				n[0].Jail.SetReason(xmrKeygenJailReason)
			}
			now := time.Unix(100, 0)
			first := a.checkKeygenRecovery(v, n, m, e, now)
			if first.Continue || (first.Error != nil) != wantError || first.Finish != wantError {
				t.Fatalf("unexpected first result: %+v", first)
			}
			last := a.checkKeygenRecovery(v, n, m, e, now.Add(xmrKeygenRecoveryStability))
			if last.Continue != (name == "recovered") || (last.Error != nil) != wantError || last.Finish != wantError {
				t.Fatalf("unexpected settled result: %+v", last)
			}
		})
	}
}

func TestXMRKeygenRecoveryRequiresContinuousStability(t *testing.T) {
	a, v, n, m, e := recoveryFixture(t)
	now := time.Unix(100, 0)
	a.checkKeygenRecovery(v, n, m, e, now)
	if result := a.checkKeygenRecovery(v, n, nil, e, now.Add(20*time.Second)); result.Continue {
		t.Fatal("missing votes must reset stability")
	}
	if result := a.checkKeygenRecovery(v, n, m, e, now.Add(31*time.Second)); result.Continue {
		t.Fatal("stability cannot span a failed observation")
	}
	if result := a.checkKeygenRecovery(v, n, m, e, now.Add(61*time.Second)); !result.Continue {
		t.Fatal("continuous recovery should pass")
	}
}
