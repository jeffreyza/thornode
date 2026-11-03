package ecdsa

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cometbft/cometbft/crypto/secp256k1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
)

// TestKeygenProofsEnabled drives a real GenerateNewKey call far enough to
// observe the *btss.Parameters that production keygen uses, without
// standing up the full P2P protocol.
//
// GenerateNewKey performs, in order:
//
//  1. GetParties / GetThreshold        — validated by valid testPubKeys.
//  2. btss.NewParameters               — constructs the params object.
//  3. NoProofMod / NoProofFac guards   — abort here if either flag is true.
//  4. preParams == nil check           — aborts with "empty pre-parameters".
//
// We pass nil preParams so the call deterministically aborts at step 4. If
// either flag had been true the call would instead abort at step 3 with one
// of the "keygen aborted: ..." sentinel errors. Asserting the error is the
// preParams one therefore proves both flags were false on the very same
// *btss.Parameters a real keygen would have used.
func TestKeygenProofsEnabled(t *testing.T) {
	conversion.SetupBech32Prefix()

	keys := append([]string(nil), testPubKeys...)
	sort.Strings(keys)

	var priv secp256k1.PrivKey = make([]byte, 32)
	kg := NewTssKeyGen(
		"",
		common.TssConfig{},
		keys[0],
		nil,
		nil,
		nil,
		"test",
		&storage.MockLocalStateManager{},
		priv,
		nil,
	)

	_, err := kg.GenerateNewKey(keygen.Request{Keys: keys})
	if err == nil {
		t.Fatal("expected an error from a TssKeyGen with no preParams")
	}

	msg := err.Error()
	if strings.Contains(msg, "Paillier-Blum (ModProof)") {
		t.Fatalf("NoProofMod was true at keygen time: %v", err)
	}
	if strings.Contains(msg, "Π-fac") {
		t.Fatalf("NoProofFac was true at keygen time: %v", err)
	}
	if !strings.Contains(msg, "empty pre-parameters") {
		t.Fatalf("keygen aborted before the preParams check for an unexpected reason — review the new failure path: %v", err)
	}
}

// TestNoProductionCodeDisablesProofs walks the thornode repo and fails if
// any non-test .go file references SetNoProofMod or SetNoProofFac as a
// selector. The runtime guard in GenerateNewKey catches such calls when
// keygen runs; this test catches them at PR time before any keygen runs.
//
// Files are parsed with go/parser and matched via *ast.SelectorExpr so
// doc-comment mentions and string-literal mentions do not create false
// positives, and the failure message includes the offending line number.
func TestNoProductionCodeDisablesProofs(t *testing.T) {
	root := repoRoot(t)
	forbidden := map[string]struct{}{
		"SetNoProofMod": {},
		"SetNoProofFac": {},
	}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "node_modules" || name == "testdata" || (len(name) > 1 && name[0] == '.') {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Logf("skipping %s (parse error: %v)", path, err)
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, hit := forbidden[sel.Sel.Name]; !hit {
				return true
			}
			pos := fset.Position(sel.Pos())
			offenders = append(offenders, fmt.Sprintf("%s:%d → %s", rel, pos.Line, sel.Sel.Name))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("production .go files must not call SetNoProofMod / SetNoProofFac:\n  %s", strings.Join(offenders, "\n  "))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}
