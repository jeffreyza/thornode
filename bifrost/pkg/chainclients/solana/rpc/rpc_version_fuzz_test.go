package rpc

import (
	"encoding/json"
	"strconv"
	"testing"
)

func FuzzRPCVersion(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte(`0`), []byte(`42`), []byte(`-1`), []byte(`"legacy"`), []byte(`null`), []byte(`{}`), []byte(`"`), []byte(`999999999999999999999999`)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1024 {
			t.Skip()
		}
		var version RPCVersion
		err := version.UnmarshalJSON(raw)
		if string(raw) == "null" && err == nil {
			t.Fatal("accepted null RPC version as numeric zero")
		}
		if err != nil {
			return
		}
		var str string
		if len(raw) > 0 && raw[0] == '"' {
			if json.Unmarshal(raw, &str) != nil || version.Value != str {
				t.Fatalf("accepted inconsistent string version %q", raw)
			}
			return
		}
		var num int
		if json.Unmarshal(raw, &num) != nil || version.Value != strconv.Itoa(num) {
			t.Fatalf("accepted inconsistent numeric version %q", raw)
		}
	})
}
