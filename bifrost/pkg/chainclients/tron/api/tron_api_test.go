package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "gopkg.in/check.v1"

	_ "embed"
)

type ApiTestSuite struct {
	server *httptest.Server
	api    *TronApi
}

var _ = Suite(&ApiTestSuite{})

func Test(t *testing.T) {
	TestingT(t)
}

func (s *ApiTestSuite) SetUpSuite(c *C) {
	s.server = NewMockServer()
	c.Assert(s.server, NotNil)
	c.Assert(s.server.URL, Not(Equals), "")

	s.api = NewTronApi(s.server.URL, time.Second)

	c.Assert(s.api, NotNil)

	fmt.Println(s.server.URL)
}

func (s *ApiTestSuite) TestGetLatestBlock(c *C) {
	block, err := s.api.GetLatestBlock()
	c.Assert(err, IsNil)
	c.Assert(block.Header.RawData.Number, Equals, int64(55088560))
	c.Assert(len(block.Transactions), Equals, 3)
}

func (s *ApiTestSuite) TestGetBlock(c *C) {
	block, err := s.api.GetBlock(55088560)
	c.Assert(err, IsNil)
	c.Assert(block.Header.RawData.Number, Equals, int64(55088560))
	c.Assert(len(block.Transactions), Equals, 3)

	// not found
	block, err = s.api.GetBlock(55088561)
	c.Assert(err, NotNil)
}

func (s *ApiTestSuite) TestGetTransactionInfo(c *C) {
	info, err := s.api.GetTransactionInfo("ec7c95841b6a3fc4ce0adfaa3fd77dd74dbe004895ad0fd02bf63eb3ca5865d5")
	c.Assert(err, IsNil)
	c.Assert(info.Fee, Equals, uint64(27704850))

	// not found
	info, err = s.api.GetTransactionInfo("d522be861c068f764c2b068be505ab6dcb52ee9435085d6c6e3255ea5b12ddb6")
	c.Assert(err, NotNil)
}

func (s *ApiTestSuite) TestGetBalance(c *C) {
	balance, err := s.api.GetBalance("TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM")
	c.Assert(err, IsNil)
	c.Assert(balance, Equals, uint64(2123313700))

	// not found
	balance, err = s.api.GetBalance("TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow")
	c.Assert(err, NotNil)
	c.Assert(balance, Equals, uint64(0))
}

func (s *ApiTestSuite) TestCreateTransaction(c *C) {
	// mocked response anyway...
	tx, err := s.api.CreateTransaction("from", "to", 4, "memo")
	c.Assert(err, IsNil)
	c.Assert(len(tx.RawData.Contract), Equals, 1)
}

func (s *ApiTestSuite) TestTriggerSmartContract(c *C) {
	tx, err := s.api.TriggerSmartContract("from", "to", "selector", "input", 123)
	c.Assert(err, IsNil)
	c.Assert(len(tx.RawData.Contract), Equals, 1)
}

func (s *ApiTestSuite) TestRehashAndBroadcastTransaction(c *C) {
	data := []byte(`
	{
	  "raw_data": {
		"contract": [
		  {
			"parameter": {
			  "type_url": "type.googleapis.com/protocol.TransferContract",
			  "value": {
				"amount": 500000000,
				"owner_address": "4102363c37fdfdbdd839b5d3df2848f176d2ae6d89",
				"to_address": "412867f90e345015cd2234f562c5767296b717157b"
			  }
			},
			"type": "TransferContract"
		  }
		]
	  },
	  "raw_data_hex": "0a0237462208a3d2c3a57b2a3a7040d8afb997cd3252312b3a74723a7474686f72316863763565636e636639676c36723267787438647265676d666d7876383063343076337772735a69080112650a2d747970652e676f6f676c65617069732e636f6d2f70726f746f636f6c2e5472616e73666572436f6e747261637412340a154102363c37fdfdbdd839b5d3df2848f176d2ae6d891215412867f90e345015cd2234f562c5767296b717157b1880cab5ee0170f8dab597cd32",
	  "ret": [
		{
		  "contractRet": "SUCCESS"
		}
	  ],
	  "txID": "ee746482c2598904b0a65e6df52a887ec7ec5565d14cb13f19b4509145b20c99"
	}`)

	var tx Transaction

	err := json.Unmarshal(data, &tx)
	c.Assert(err, IsNil)
	c.Assert(len(tx.Signature), Equals, 0)

	tx.RawData.RefBlockBytes = "3746"
	tx.RawData.RefBlockHash = "a3d2c3a57b2a3a70"
	tx.RawData.Timestamp = 1738705563000
	tx.RawData.Expiration = 1738705623000
	tx.RawData.Data = "2b3a74723a7474686f72316863763565636e636639676c36723267787438647265676d666d787638306334307633777273"

	err = tx.Rehash()
	c.Assert(err, IsNil)
	c.Assert(tx.TxId, Equals, "e1cd4454d71d8973e89155bc2c2a91aa56fd470c8ce64547ce8c69c789c21f0c")

	txBytes, err := json.Marshal(tx)
	c.Assert(err, IsNil)
	c.Assert(txBytes, NotNil)

	response, err := s.api.BroadcastTransaction(txBytes)
	c.Assert(err, IsNil)
	c.Assert(response.TxId, Equals, "77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299")
}

func (s *ApiTestSuite) TestGetChainParameters(c *C) {
	params, err := s.api.GetChainParameters()
	c.Assert(err, IsNil)
	c.Assert(params.EnergyFee, Equals, int64(210))
}

func (s *ApiTestSuite) TestEstimateEnergy(c *C) {
	energy, err := s.api.EstimateEnergy("from", "contract", "selector", "input")
	c.Assert(err, IsNil)
	c.Assert(energy, Equals, int64(900000))
}

func (s *ApiTestSuite) TestConvertAddress(c *C) {
	testCases := []struct {
		In  string
		Out string
	}{
		{
			In:  "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
			Out: "41e2f72d7827bd1efbb6d55d6ffb1db94aab37b587",
		},
		{
			In:  "41e2f72d7827bd1efbb6d55d6ffb1db94aab37b587",
			Out: "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
		},
		{
			In:  "0xe2f72d7827bd1efbb6d55d6ffb1db94aab37b587",
			Out: "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
		},
		{
			In:  "bc1qelysxslyl86yq57yx9lzhl50pfayj9wng54p3g",
			Out: "",
		},
		{
			In:  "",
			Out: "",
		},
	}

	for _, tc := range testCases {
		address, err := ConvertAddress(tc.In)
		if tc.Out == "" {
			c.Assert(err, NotNil)
			c.Assert(address, Equals, tc.Out)
			c.Assert(err, ErrorMatches, "address not valid")
			continue
		}
		c.Assert(err, IsNil)
		c.Assert(address, Equals, tc.Out)
	}
}

func TestGetTransactionInfoRejectsErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
		wantID     string
	}{
		{"absent", `{}`, false, ""},
		{"included", `{"id":"original-tx","blockNumber":86}`, false, "original-tx"},
		{"rate limited", `{"Error":"class java.lang.IllegalAccessException : lack of computing resources"}`, true, ""},
		{"error with receipt", `{"Error":"lookup failed","id":"original-tx","blockNumber":86}`, true, ""},
		{"empty error", `{"Error":""}`, true, ""},
		{"null response", `null`, true, ""},
		{"malformed response", `{`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/wallet/gettransactioninfobyid" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewTronApi(server.URL, time.Second)
			info, err := client.GetTransactionInfo("original-tx")
			if (err != nil) != tc.wantError {
				t.Fatalf("receipt error = %v, want error %v", err, tc.wantError)
			}
			if info.ID != tc.wantID {
				t.Fatalf("receipt ID = %q, want %q", info.ID, tc.wantID)
			}
		})
	}
}

func TestGetSolidBlockHeight(t *testing.T) {
	blockID := fmt.Sprintf("%016x%048x", 123, 0xabcdef)
	validBlock := "Num:123,ID:" + blockID
	for _, tc := range []struct {
		name   string
		body   string
		status int
		want   int64
	}{
		{name: "solidified", body: `{"solidityBlock":"` + validBlock + `"}`, want: 123},
		{name: "uppercase block ID", body: `{"solidityBlock":"Num:123,ID:` + strings.ToUpper(blockID) + `"}`, want: 123},
		{name: "head is not finality", body: `{"block":"` + validBlock + `"}`},
		{name: "empty object", body: `{}`},
		{name: "null", body: `null`},
		{name: "malformed JSON", body: `{`},
		{name: "wrong field type", body: `{"solidityBlock":123}`},
		{name: "RPC error", body: `{"solidityBlock":"` + validBlock + `"}`, status: http.StatusServiceUnavailable},
		{name: "application error", body: `{"solidityBlock":"` + validBlock + `","Error":"unavailable"}`},
		{name: "empty application error", body: `{"solidityBlock":"` + validBlock + `","Error":""}`},
		{name: "null application error", body: `{"solidityBlock":"` + validBlock + `","Error":null}`},
		{name: "missing prefix", body: `{"solidityBlock":"123,ID:` + blockID + `"}`},
		{name: "missing ID", body: `{"solidityBlock":"Num:123"}`},
		{name: "zero", body: `{"solidityBlock":"Num:0,ID:` + strings.Repeat("0", 64) + `"}`},
		{name: "negative", body: `{"solidityBlock":"Num:-1,ID:` + blockID + `"}`},
		{name: "overflow", body: `{"solidityBlock":"Num:9223372036854775808,ID:` + blockID + `"}`},
		{name: "nondecimal", body: `{"solidityBlock":"Num:0x7b,ID:` + blockID + `"}`},
		{name: "leading zero", body: `{"solidityBlock":"Num:0123,ID:` + blockID + `"}`},
		{name: "plus sign", body: `{"solidityBlock":"Num:+123,ID:` + blockID + `"}`},
		{name: "height ID mismatch", body: `{"solidityBlock":"Num:124,ID:` + blockID + `"}`},
		{name: "short ID", body: `{"solidityBlock":"Num:123,ID:00"}`},
		{name: "nonhex ID", body: `{"solidityBlock":"Num:123,ID:` + strings.Repeat("z", 64) + `"}`},
		{name: "trailing data", body: `{"solidityBlock":"` + validBlock + ` garbage"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/wallet/getnodeinfo" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewTronApi(server.URL, time.Second)
			height, err := client.GetSolidBlockHeight()
			if (err == nil) != (tc.want != 0) || height != tc.want {
				t.Fatalf("height = %d, error = %v, want %d", height, err, tc.want)
			}
		})
	}
}

func TestGetTransactionRejectsAmbiguousResponses(t *testing.T) {
	const txID = "deadbeef9034d95770d5704c9898e3414780c5fcbe282350080db8c4cef3e729"
	for _, tc := range []struct {
		name    string
		body    string
		status  int
		wantID  string
		wantErr bool
	}{
		{name: "absent", body: `{}`},
		{name: "absent whitespace", body: " { } \n"},
		{name: "found", body: `{"txID":"` + txID + `"}`, wantID: txID},
		{name: "null", body: `null`, wantErr: true},
		{name: "malformed", body: `{`, wantErr: true},
		{name: "empty body", body: ``, wantErr: true},
		{name: "array", body: `[]`, wantErr: true},
		{name: "missing ID", body: `{"raw_data":{}}`, wantErr: true},
		{name: "empty ID", body: `{"txID":""}`, wantErr: true},
		{name: "null ID", body: `{"txID":null}`, wantErr: true},
		{name: "wrong ID type", body: `{"txID":123}`, wantErr: true},
		{name: "error", body: `{"Error":"unavailable"}`, wantErr: true},
		{name: "error with ID", body: `{"txID":"` + txID + `","Error":"unavailable"}`, wantErr: true},
		{name: "empty error with ID", body: `{"txID":"` + txID + `","Error":""}`, wantErr: true},
		{name: "null error with ID", body: `{"txID":"` + txID + `","Error":null}`, wantErr: true},
		{name: "lowercase error with ID", body: `{"txID":"` + txID + `","error":"unavailable"}`, wantErr: true},
		{name: "HTTP not found", body: `{}`, status: http.StatusNotFound, wantErr: true},
		{name: "HTTP failure", body: `{}`, status: http.StatusServiceUnavailable, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/wallet/gettransactionbyid" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var request struct {
					Value string `json:"value"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Value != txID {
					t.Errorf("transaction request = %q, error = %v", request.Value, err)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewTronApi(server.URL, time.Second)
			tx, err := client.GetTransaction(txID)
			if (err != nil) != tc.wantErr || tx.TxId != tc.wantID {
				t.Fatalf("transaction ID = %q, error = %v, want %q, error %v", tx.TxId, err, tc.wantID, tc.wantErr)
			}
		})
	}
}

func TestTransactionInfoExecutionResult(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantResult, wantReceipt string
		wantErr                             bool
	}{
		{name: "failed", body: `{"result":"FAILED","receipt":{"result":"OUT_OF_ENERGY"}}`, wantResult: "FAILED", wantReceipt: "OUT_OF_ENERGY"},
		{name: "success", body: `{"receipt":{"result":"SUCCESS"}}`, wantReceipt: "SUCCESS"},
		{name: "missing receipt", body: `{"result":"FAILED"}`, wantResult: "FAILED"},
		{name: "unknown", body: `{"receipt":{"result":"UNKNOWN"}}`, wantReceipt: "UNKNOWN"},
		{name: "malformed result", body: `{"result":1}`, wantErr: true},
		{name: "malformed receipt result", body: `{"receipt":{"result":10}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/wallet/gettransactioninfobyid" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewTronApi(server.URL, time.Second)
			info, err := client.GetTransactionInfo("recorded-tx")
			if (err != nil) != tc.wantErr || info.Result != tc.wantResult || info.Receipt.Result != tc.wantReceipt {
				t.Fatalf("execution result = %q, receipt result = %q, error = %v", info.Result, info.Receipt.Result, err)
			}
		})
	}
}
