package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/CosmWasm/wasmd/x/wasm/ioutils"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/hashicorp/go-multierror"
	"github.com/mitchellh/mapstructure"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/luvicheck"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v3"
)

////////////////////////////////////////////////////////////////////////////////////////
// Template
////////////////////////////////////////////////////////////////////////////////////////

// opFuncMap returns a routine-scoped function map used to render template expressions
// passed through from the outer rendering - variables dependent on execution state.
func opFuncMap(routine int) template.FuncMap {
	return template.FuncMap{
		"native_txid": func(i int) string {
			nativeTxIDsMu.Lock()
			defer nativeTxIDsMu.Unlock()
			if i < 0 {
				i += len(nativeTxIDs[routine]) + 1
			}
			return nativeTxIDs[routine][i-1]
		},
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Operation
////////////////////////////////////////////////////////////////////////////////////////

type Operation interface {
	Execute(out io.Writer, path string, line, routine int, thornode *os.Process, logs chan string) error
	OpType() string
}

type OpBase struct {
	Type string `json:"type"`
}

func (op *OpBase) OpType() string {
	return op.Type
}

func NewOperation(opMap map[string]any) (Operation, error) {
	// ensure type is provided
	t, ok := opMap["type"].(string)
	if !ok {
		return nil, fmt.Errorf("operation type is not a string: %v", opMap["type"])
	}

	// create the operation for the type
	var op Operation
	switch t {
	case "env":
		op = &OpEnv{}
	case "state":
		op = &OpState{}
	case "check":
		op = &OpCheck{}
	case "create-blocks":
		op = &OpCreateBlocks{}
	case "tx-ban":
		op = &OpTxBan{}
	case "tx-deposit":
		op = &OpTxDeposit{}
	case "tx-errata-tx":
		op = &OpTxErrataTx{}
	case "tx-mimir":
		op = &OpTxMimir{}
	case "tx-observed-in":
		op = &OpTxObservedIn{}
	case "tx-observed-out":
		op = &OpTxObservedOut{}
	case "tx-network-fee":
		op = &OpTxNetworkFee{}
	case "tx-price-feed":
		op = &OpTxPriceFeed{}
	case "tx-node-pause-chain":
		op = &OpTxNodePauseChain{}
	case "tx-send":
		op = &OpTxSend{}
	case "tx-set-ip-address":
		op = &OpTxSetIPAddress{}
	case "tx-set-node-keys":
		op = &OpTxSetNodeKeys{}
	case "tx-solvency":
		op = &OpTxSolvency{}
	case "tx-tss-keysign":
		op = &OpTxTssKeysign{}
	case "tx-tss-pool":
		op = &OpTxTssPool{}
	case "tx-version":
		op = &OpTxVersion{}
	case "tx-bank-send":
		op = &OpTxBankSend{}
	case "fail-export-invariants":
		op = &OpFailExportInvariants{}
	case "tx-propose-upgrade":
		op = &OpTxProposeUpgrade{}
	case "tx-approve-upgrade":
		op = &OpTxApproveUpgrade{}
	case "tx-reject-upgrade":
		op = &OpTxRejectUpgrade{}

	// x/wasm
	case "tx-execute-contract":
		op = &OpTxExecuteContract{}
	case "tx-instantiate-contract-2":
		op = &OpTxInstantiateContract2{}
	case "tx-instantiate-contract":
		op = &OpTxInstantiateContract{}
	case "tx-migrate-contract":
		op = &OpTxMigrateContract{}
	case "tx-store-code":
		op = &OpTxStoreCode{}
	case "tx-sudo-contract":
		op = &OpTxSudoContract{}
	case "tx-update-admin":
		op = &OpTxUpdateAdmin{}
	case "tx-clear-admin":
		op = &OpTxClearAdmin{}

	default:
		return nil, fmt.Errorf("unknown operation type: %s", t)
	}

	// create decoder supporting embedded structs and weakly typed input
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:          "json",
		WeaklyTypedInput: true,
		ErrorUnused:      true,
		Squash:           true,
		Result:           op,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create decoder: %w", err)
	}

	switch op.(type) {
	// internal types have MarshalJSON methods necessary to decode
	case *OpTxBan,
		*OpTxErrataTx,
		*OpTxObservedIn,
		*OpTxObservedOut,
		*OpTxDeposit,
		*OpTxMimir,
		*OpTxNetworkFee,
		*OpTxNodePauseChain,
		*OpTxSolvency,
		*OpTxSend,
		*OpTxSetIPAddress,
		*OpTxSetNodeKeys,
		*OpTxVersion,
		*OpTxProposeUpgrade,
		*OpTxApproveUpgrade,
		*OpTxRejectUpgrade,
		*OpTxTssKeysign,
		*OpTxTssPool,
		*OpTxBankSend,
		*OpTxExecuteContract,
		*OpTxInstantiateContract,
		*OpTxInstantiateContract2,
		*OpTxSudoContract,
		*OpTxStoreCode:
		// encode as json
		buf := bytes.NewBuffer(nil)
		enc := json.NewEncoder(buf)
		err = enc.Encode(opMap)
		if err != nil {
			return nil, fmt.Errorf("failed to encode operation: %w", err)
		}

		// unmarshal json to op
		err = json.NewDecoder(buf).Decode(op)

	default:
		err = dec.Decode(opMap)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to decode operation: %w", err)
	}

	// default status check to 200 if endpoint is set
	var oc *OpCheck
	if oc, ok = op.(*OpCheck); ok && oc.Endpoint != "" {
		if oc.Status == 0 {
			oc.Status = 200
		}
	}

	return op, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// OpEnv
////////////////////////////////////////////////////////////////////////////////////////

type OpEnv struct {
	OpBase `yaml:",inline"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

func (op *OpEnv) Execute(_ io.Writer, _ string, _, _ int, _ *os.Process, _ chan string) error {
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// OpState
////////////////////////////////////////////////////////////////////////////////////////

type OpState struct {
	OpBase  `yaml:",inline"`
	Genesis map[string]any `json:"genesis"`
}

func (op *OpState) Execute(_ io.Writer, _ string, _, routine int, _ *os.Process, _ chan string) error {
	// extract HOME from command environment
	home := regtestHome(routine)

	// load genesis file
	f, err := os.OpenFile(filepath.Join(home, ".thornode/config/genesis.json"), os.O_RDWR, 0o644)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open genesis file")
	}

	// unmarshal genesis into map
	var genesisMap map[string]any
	err = json.NewDecoder(f).Decode(&genesisMap)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to decode genesis file")
	}

	// merge updates into genesis
	genesis := deepMerge(genesisMap, op.Genesis, "address", "denom")

	// reset file
	err = f.Truncate(0)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to truncate genesis file")
	}
	_, err = f.Seek(0, 0)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to seek genesis file")
	}

	// marshal genesis into file
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(genesis)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to encode genesis file")
	}

	return f.Close()
}

////////////////////////////////////////////////////////////////////////////////////////
// OpCheck
////////////////////////////////////////////////////////////////////////////////////////

type OpCheck struct {
	OpBase   `yaml:",inline"`
	Endpoint string            `json:"endpoint"`
	Params   map[string]string `json:"params"`
	Status   int               `json:"status"`
	Asserts  []string          `json:"asserts"`

	prefetchResp *http.Response
	prefetchErr  error
}

func (op *OpCheck) prefetch(routine int) {
	// abort if no endpoint is set (empty check op is allowed for breakpoint convenience)
	if op.Endpoint == "" {
		op.prefetchErr = errors.New("check")
		return
	}

	tmpl := template.Must(template.Must(templates.Clone()).Funcs(opFuncMap(routine)).Parse(op.Endpoint))
	expr := bytes.NewBuffer(nil)
	err := tmpl.Execute(expr, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to render assert expression")
	}
	op.Endpoint = expr.String()

	// build request
	req, err := http.NewRequest("GET", op.Endpoint, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to build request")
	}

	// parse the endpoint and add routine to the port number
	port, err := strconv.Atoi(req.URL.Port())
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse port")
	}
	if req.URL.Hostname() != "localhost" { // host must be localhost
		log.Fatal().Str("host", req.URL.Hostname()).Msg("endpoint host must be localhost")
	}
	req.URL.Host = fmt.Sprintf("localhost:%d", port+routine)

	// add params
	q := req.URL.Query()
	for k, v := range op.Params {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()

	// Transaction indexing can briefly lag block creation on loaded CI runners.
	for i := 0; i < 10; i++ {
		op.prefetchResp, op.prefetchErr = httpClient.Do(req)
		if op.prefetchErr != nil || op.prefetchResp.StatusCode != http.StatusNotFound || op.Status == http.StatusNotFound || i == 9 {
			return
		}
		op.prefetchResp.Body.Close()
		time.Sleep(100 * time.Millisecond * getTimeFactor())
	}
}

func (op *OpCheck) Execute(out io.Writer, path string, line, routine int, _ *os.Process, logs chan string) error {
	localLog := consoleLogger(out)

	// in AUTO_UPDATE mode prefetch is not run
	if os.Getenv("AUTO_UPDATE") != "" {
		op.prefetch(routine)
	}

	if op.prefetchErr != nil {
		localLog.Err(op.prefetchErr).Msg("prefetch check failed")
		return op.prefetchErr
	}

	// read response
	buf, err := io.ReadAll(op.prefetchResp.Body)
	if err != nil {
		localLog.Err(err).Msg("failed to read response")
		return err
	}

	// ensure status code matches
	if op.prefetchResp.StatusCode != op.Status {
		// dump pretty output for debugging
		_, _ = out.Write([]byte(ColorPurple + "\nOperation:" + ColorReset + "\n"))
		_ = yaml.NewEncoder(out).Encode(op)
		_, _ = out.Write([]byte(ColorPurple + "\nEndpoint Response:" + ColorReset + "\n"))
		_, _ = out.Write([]byte(string(buf) + "\n"))

		return fmt.Errorf("unexpected status code: %d", op.prefetchResp.StatusCode)
	}

	// ensure response is not empty
	if len(buf) == 0 {
		if os.Getenv("DEBUG") == "" {
			fmt.Println(ColorPurple + "\nLogs:" + ColorReset)
			dumpLogs(out, logs)
		}

		fmt.Println(ColorPurple + "\nOperation:" + ColorReset)
		_ = yaml.NewEncoder(os.Stdout).Encode(op)
		fmt.Println()
		return fmt.Errorf("empty response")
	}

	updates := make(map[string][2]string)
	failedAsserts := make([]string, 0)

	// pipe response to jq for assertions
	for _, a := range op.Asserts {
		// render the assert expression (used for native_txid)
		tmpl := template.Must(template.Must(templates.Clone()).Funcs(opFuncMap(routine)).Parse(a))
		expr := bytes.NewBuffer(nil)
		err = tmpl.Execute(expr, nil)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to render assert expression")
		}
		a = expr.String()

		cmd := exec.Command("jq", "-e", a)
		cmd.Stdin = bytes.NewReader(buf)
		var cmdOut []byte
		cmdOut, err = cmd.CombinedOutput()
		if err != nil {
			failedAsserts = append(failedAsserts, a)

			// attempt to auto update values if configured
			if os.Getenv("AUTO_UPDATE") != "" {
				if !strings.Contains(a, "==") {
					continue
				}

				// extract expression and value on last ==
				lastIdx := strings.LastIndex(a, "==")
				expr := a[:lastIdx-1]
				value := a[lastIdx+2:]

				// evaluate the expression
				evalCmd := exec.Command("jq", "-r", expr)
				evalCmd.Stdin = bytes.NewReader(buf)
				var evalOut []byte
				evalOut, err = evalCmd.CombinedOutput()
				if err != nil {
					localLog.Err(err).Msg("failed to evaluate expression")
					continue
				}

				// record the mapping and continue
				value = strings.Trim(strings.TrimSpace(value), `"`)
				updates[expr] = [2]string{value, strings.TrimSpace(string(evalOut))}
				continue
			}

			// log fatal on syntax errors and skip logs
			if cmd.ProcessState.ExitCode() != 1 {
				drainLogs(logs)
				_, _ = out.Write([]byte(ColorRed + string(cmdOut) + ColorReset + "\n"))
			}
		}
	}

	if len(failedAsserts) > 0 {
		// dump process logs if an assert failed
		_, _ = out.Write([]byte(ColorPurple + "\nLogs:" + ColorReset + "\n"))
		dumpLogs(out, logs)

		// dump pretty output for debugging
		_, _ = out.Write([]byte(ColorPurple + "\nOperation:" + ColorReset + "\n"))
		_ = yaml.NewEncoder(out).Encode(op)
		_, _ = out.Write([]byte(ColorPurple + "\nEndpoint Response:" + ColorReset + "\n"))
		_, _ = out.Write([]byte(string(buf) + "\n\n"))
		for _, a := range failedAsserts {
			_, _ = out.Write([]byte(ColorPurple + "Failed Assert: " + ColorReset + a + "\n"))
		}
		_, _ = out.Write([]byte("\n"))

		if os.Getenv("IGNORE_FAILURES") == "" && os.Getenv("AUTO_UPDATE") == "" {
			return fmt.Errorf("%d failed asserts", len(failedAsserts))
		}
	}

	// update the test with new values
	if len(updates) > 0 {
		// load the test file
		var data []byte
		data, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		dataStr := string(data)

		// now find the assert that matches
		lines := strings.Split(dataStr, "\n")

		// update the values
		for expr, vals := range updates {
			oldVal := vals[0]
			newVal := vals[1]

			found := false
			for i := line; i < len(lines); i++ {
				if strings.Contains(lines[i], expr) && strings.Contains(lines[i], oldVal) {
					found = true
					clog := localLog.With().
						Int("line", i).
						Str("expr", expr).
						Str("old", oldVal).
						Str("new", newVal).
						Logger()

					// report error and skip if the new value is null or empty
					switch {
					case newVal == "" || newVal == "null":
						clog.Error().Msg("skipping auto update on null value")
					case strings.HasSuffix(expr, "length"):
						clog.Warn().Msg("skipping auto update on length")
					default:
						clog.Info().Msg("auto updating value")
						lines[i] = strings.ReplaceAll(lines[i], oldVal, newVal)
					}
					break
				}
			}
			if !found {
				localLog.Warn().
					Str("expr", expr).
					Str("old", oldVal).
					Str("new", newVal).
					Msg("failed to find assertion to update")
			}
		}

		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
	}

	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// OpCreateBlocks
////////////////////////////////////////////////////////////////////////////////////////

type OpCreateBlocks struct {
	OpBase         `yaml:",inline"`
	Count          int      `json:"count"`
	SkipInvariants bool     `json:"skip_invariants"`
	Exit           *int     `json:"exit"`
	LuviDrop       []string `json:"luvi_drop"`
}

func (op *OpCreateBlocks) Execute(out io.Writer, path string, _, routine int, p *os.Process, logs chan string) error {
	localLog := consoleLogger(out)

	// clear existing log output
	drainLogs(logs)

	// the loop below trims path in place for blockPath; record block times
	// under the untrimmed suite path on every iteration
	suite := path

	// record the state the blocks start from, so the first block is checked
	// too; before the first block the state is not queryable, and that block
	// becomes the baseline instead
	var luviViolations []luvicheck.Violation
	if op.Exit == nil {
		if v, err := observeLuvi(routine, 0, &openapi.BlockResponse{}); err == nil {
			luviViolations = append(luviViolations, v...)
		}
	}

	for i := 0; i < op.Count; i++ {
		// http request to localhost to unblock block creation
		blockStart := time.Now()
		newBlockRes, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/newBlock", regtestCreateBlockPort(routine)))
		blockElapsed := time.Since(blockStart)
		if err != nil {
			// if exit code is not set this was unexpected
			if op.Exit == nil {
				localLog.Err(err).Msg("failed to create block")
				return err
			}

			// if exit code is set, this was expected
			if processRunning(p.Pid) {
				localLog.Err(err).Msg("block did not exit as expected")
				return err
			}

			// if process is not running, check exit code
			var ps *os.ProcessState
			ps, err = p.Wait()
			if err != nil {
				localLog.Err(err).Msg("failed to wait for process")
				return err
			}
			if ps.ExitCode() != *op.Exit {
				localLog.Error().Int("exit", ps.ExitCode()).Int("expect", *op.Exit).Msg("bad exit code")
				return err
			}

			// exit code is correct, return nil
			return nil
		}

		// parse height from response
		body, err := io.ReadAll(newBlockRes.Body)
		if err != nil {
			localLog.Err(err).Msg("failed to read new block response")
			return err
		}
		newBlockRes.Body.Close()
		height, err := strconv.Atoi(string(body))
		if err != nil {
			localLog.Err(err).Msg("failed to parse new block response")
			return err
		}
		recordBlockTime(suite, height, blockElapsed)

		// determine path for block output
		path = strings.TrimPrefix(path, "suites/")
		path = strings.TrimSuffix(path, filepath.Ext(path))
		blockPath := filepath.Join(regtestMntDir(), "blocks", path, fmt.Sprintf("%03d.json", height))

		// make block directory
		err = os.MkdirAll(filepath.Dir(blockPath), 0o755)
		if err != nil {
			localLog.Err(err).Str("path", blockPath).Msg("failed to create block directory")
			return err
		}

		// truncate or create file
		f, err := os.OpenFile(blockPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			localLog.Err(err).Str("path", blockPath).Msg("failed to create block file")
			return err
		}

		// avoid minor raciness after end block
		time.Sleep(100 * time.Millisecond * getTimeFactor())

		// get the block response
		url := fmt.Sprintf("http://localhost:%d/thorchain/block?height=%d", 1317+routine, height)
		var res *http.Response
		for j := 0; j < 5; j++ {
			res, err = httpClient.Get(url)
			if err == nil {
				if res.StatusCode == http.StatusOK {
					break
				}
				_ = res.Body.Close()
			}
			time.Sleep(100 * time.Millisecond * getTimeFactor())
		}
		if err != nil {
			localLog.Err(err).Msg("failed to get block")
			return err
		}
		if res.StatusCode != http.StatusOK {
			localLog.Error().Int("status", res.StatusCode).Msg("failed to get block")
			return fmt.Errorf("failed to get block: %d", res.StatusCode)
		}

		// decode response
		blockResponse := &openapi.BlockResponse{}
		err = json.NewDecoder(res.Body).Decode(blockResponse)
		if err != nil {
			localLog.Err(err).Msg("failed to decode block response")
			return err
		}

		// zero non-deterministic fields
		blockResponse.Id = openapi.BlockResponseId{}
		blockResponse.Header.Time = ""
		blockResponse.Header.LastBlockId = openapi.BlockResponseId{}
		blockResponse.Header.LastCommitHash = ""

		// only apply static values if UNFILTER_EXPORT is not set
		if os.Getenv("UNFILTER_EXPORT") == "" {
			blankHash := "0000000000000000000000000000000000000000000000000000000000000000"
			blockResponse.Header.AppHash = blankHash
			blockResponse.Header.LastResultsHash = blankHash

			// zero gas_used for all transactions
			zeroGas := "0"
			for i := range blockResponse.Txs {
				blockResponse.Txs[i].Result.GasUsed = &zeroGas
			}
		}

		// write to file
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		err = enc.Encode(blockResponse)
		if err != nil {
			localLog.Err(err).Msg("failed to encode block response")
			return err
		}

		// close the file and response
		_ = f.Close()
		_ = res.Body.Close()

		v, err := observeLuvi(routine, int64(height), blockResponse)
		if err != nil {
			localLog.Err(err).Msg("failed to observe luvi")
			return err
		}
		luviViolations = append(luviViolations, v...)
	}

	// if exit code is set, this was unexpected
	if op.Exit != nil {
		localLog.Error().Int("expect", *op.Exit).Msg("expected exit code")
		return errors.New("expected exit code")
	}

	if err := checkLuvi(luviViolations, op.LuviDrop); err != nil {
		return err
	}

	if op.SkipInvariants {
		return nil
	}
	return checkInvariants(routine)
}

// ------------------------------ invariants ------------------------------

func checkInvariants(routine int) error {
	api := fmt.Sprintf("http://localhost:%d", 1317+routine)

	wg := sync.WaitGroup{}
	mu := sync.Mutex{}
	var returnErr error

	for _, inv := range invariants {
		wg.Add(1)
		go func(inv string) {
			defer wg.Done()

			endpoint := fmt.Sprintf("%s/thorchain/invariant/%s", api, inv)
			req, err := http.NewRequest("GET", endpoint, nil)
			if err != nil {
				mu.Lock()
				returnErr = multierror.Append(returnErr, err)
				mu.Unlock()
				return
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				mu.Lock()
				returnErr = multierror.Append(returnErr, err)
				mu.Unlock()
				return
			}

			// If for instance the endpoint panics, report the error
			// rather than treating it as no broken invariants.
			if resp.StatusCode != http.StatusOK {
				defer resp.Body.Close()
				var body []byte
				body, err = io.ReadAll(resp.Body)
				mu.Lock()
				if err == nil {
					returnErr = multierror.Append(returnErr, fmt.Errorf("invariant %s status code %d response: %s", inv, resp.StatusCode, string(body)))
				} else {
					returnErr = multierror.Append(returnErr, fmt.Errorf("invariant %s status code %d error: %s", inv, resp.StatusCode, err.Error()))
				}
				mu.Unlock()
				return
			}

			invRes := struct {
				Broken    bool
				Invariant string
				Msg       []string
			}{}
			if err = json.NewDecoder(resp.Body).Decode(&invRes); err != nil {
				mu.Lock()
				returnErr = multierror.Append(returnErr, err)
				mu.Unlock()
				return
			}
			if invRes.Broken {
				err = fmt.Errorf("%s invariant is broken: %v", inv, invRes.Msg)
				mu.Lock()
				returnErr = multierror.Append(returnErr, err)
				mu.Unlock()
				return
			}
		}(inv)
	}
	wg.Wait()

	return returnErr
}

////////////////////////////////////////////////////////////////////////////////////////
// Transaction Operations
////////////////////////////////////////////////////////////////////////////////////////

// ------------------------------ OpTxBan ------------------------------

type OpTxBan struct {
	OpBase      `yaml:",inline"`
	NodeAddress sdk.AccAddress `json:"node_address"`
	Signer      sdk.AccAddress `json:"signer"`
	Sequence    *int64         `json:"sequence"`
	Gas         *int64         `json:"gas"`
}

func (op *OpTxBan) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgBan(op.NodeAddress, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxErrataTx ------------------------------

type OpTxErrataTx struct {
	OpBase   `yaml:",inline"`
	TxID     common.TxID    `json:"tx_id"`
	Chain    common.Chain   `json:"chain"`
	Signer   sdk.AccAddress `json:"signer"`
	Sequence *int64         `json:"sequence"`
	Gas      *int64         `json:"gas"`
}

func (op *OpTxErrataTx) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgErrataTx(op.TxID, op.Chain, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxNetworkFee ------------------------------

type OpTxNetworkFee struct {
	OpBase          `yaml:",inline"`
	BlockHeight     int64          `json:"block_height"`
	Chain           common.Chain   `json:"chain"`
	TransactionSize uint64         `json:"transaction_size"`
	TransactionRate uint64         `json:"transaction_rate"`
	Signer          sdk.AccAddress `json:"signer"`
	Sequence        *int64         `json:"sequence"`
	Gas             *int64         `json:"gas"`
}

func (op *OpTxNetworkFee) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgNetworkFee(op.BlockHeight, op.Chain, op.TransactionSize, op.TransactionRate, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxPriceFeed ------------------------------

// priceFeedTime is a fixed, far-future timestamp (2100-01-01 UTC, milliseconds)
// used for every price-feed attestation. The handler rejects feeds whose time is
// before the block header time, so this must stay ahead of any regression block
// time; a fixed value also keeps the signed payload deterministic across runs.
const priceFeedTime int64 = 4102444800000

// OpTxPriceFeed seeds oracle prices by sending a signed QuorumPriceFeedBatch to
// the node's Enshrined Bifrost (ebifrost) over its localhost gRPC interface, the
// same path bifrost uses in production. MsgPriceFeedQuorumBatch cannot be
// broadcast as a normal tx (the InjectedTxDecorator ante rejects it) - it is only
// admitted when the proposer injects it during PrepareProposal. ebifrost caches
// the batch (30m TTL) and re-injects it every block, so a suite sends this once
// and oracle prices stay live for the remainder of the run (they are otherwise
// wiped every BeginBlock by OracleMgr). The injected tx is processed before
// mempool txs in the block. Only symbols in the RequiredPriceFeeds constant can
// be priced (e.g. USDC, USDT); unlisted symbols default to a zero rate, which the
// handler discards.
type OpTxPriceFeed struct {
	OpBase `yaml:",inline"`
	Prices map[string]string `json:"prices"` // symbol -> decimal price, e.g. {"USDC": "1.00"}
	Node   string            `json:"node"`   // attesting active-node name, default "dog"
}

func (op *OpTxPriceFeed) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	node := op.Node
	if node == "" {
		node = "dog"
	}

	privKey, err := nodeSecpPrivKey(node)
	if err != nil {
		return err
	}

	// The handler derives the required symbols and the feed version from the
	// RequiredPriceFeeds constant; mirror it exactly so attestations validate.
	// (A suite that overrides RequiredPriceFeeds via mimir would change the
	// version hash and is therefore unsupported.)
	feedConfig := constants.NewConstantValue().GetStringValue(constants.RequiredPriceFeeds)
	symbols := strings.Split(feedConfig, ",")
	version := sha256.Sum256([]byte(feedConfig))

	rates := make([]*common.OraclePrice, len(symbols))
	for i, symbol := range symbols {
		value := big.NewFloat(0)
		if priceStr, ok := op.Prices[symbol]; ok {
			parsed, _, parseErr := big.ParseFloat(priceStr, 10, 0, big.ToNearestEven)
			if parseErr != nil {
				return fmt.Errorf("failed to parse price for %s: %w", symbol, parseErr)
			}
			value = parsed
		}
		rate, rateErr := common.NewOraclePrice(value)
		if rateErr != nil {
			return fmt.Errorf("failed to build oracle price for %s: %w", symbol, rateErr)
		}
		rates[i] = rate
	}

	priceFeed := &common.PriceFeed{
		Version: version[:8],
		Time:    priceFeedTime,
		Rates:   rates,
	}

	signBz, err := priceFeed.GetSignablePayload()
	if err != nil {
		return err
	}
	signature, err := privKey.Sign(signBz)
	if err != nil {
		return err
	}

	batch := &common.QuorumPriceFeedBatch{
		QuorumPriceFeeds: []*common.QuorumPriceFeed{{
			PriceFeed: priceFeed,
			Attestations: []*common.Attestation{{
				PubKey:    privKey.PubKey().Bytes(),
				Signature: signature,
			}},
		}},
	}

	// Deliver to the node's ebifrost gRPC (same container, port offset by routine).
	addr := fmt.Sprintf("127.0.0.1:%d", regtestEbifrostPort(routine))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to dial ebifrost at %s: %w", addr, err)
	}
	defer conn.Close()

	grpcCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = ebifrost.NewLocalhostBifrostClient(conn).SendQuorumPriceFeedBatch(grpcCtx, batch); err != nil {
		return fmt.Errorf("failed to send price feed to ebifrost: %w", err)
	}

	localLog := log.Output(zerolog.ConsoleWriter{Out: out})
	localLog.Debug().Str("node", node).Msg("price feed sent to ebifrost")
	return nil
}

// ------------------------------ OpTxNodePauseChain ------------------------------

type OpTxNodePauseChain struct {
	OpBase   `yaml:",inline"`
	Value    int64          `json:"value"`
	Signer   sdk.AccAddress `json:"signer"`
	Sequence *int64         `json:"sequence"`
	Gas      *int64         `json:"gas"`
}

func (op *OpTxNodePauseChain) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgNodePauseChain(op.Value, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxObservedIn ------------------------------

type OpTxObservedIn struct {
	OpBase   `yaml:",inline"`
	Txs      []common.ObservedTx `json:"txs"`
	Signer   sdk.AccAddress      `json:"signer"`
	Sequence *int64              `json:"sequence"`
	Gas      *int64              `json:"gas"`
}

func (op *OpTxObservedIn) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgObservedTxIn(op.Txs, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxObservedOut ------------------------------

type OpTxObservedOut struct {
	OpBase   `yaml:",inline"`
	Txs      []common.ObservedTx `json:"txs"`
	Signer   sdk.AccAddress      `json:"signer"`
	Sequence *int64              `json:"sequence"`
	Gas      *int64              `json:"gas"`
}

func (op *OpTxObservedOut) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	// render the memos (used for native_txid)
	for i, tx := range op.Txs {
		tmpl := template.Must(template.Must(templates.Clone()).Funcs(opFuncMap(routine)).Parse(tx.Tx.Memo))
		memo := bytes.NewBuffer(nil)
		err := tmpl.Execute(memo, nil)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to render memo")
		}
		tx.Tx.Memo = memo.String()
		op.Txs[i] = tx
	}

	msg := types.NewMsgObservedTxOut(op.Txs, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxSetIPAddress ------------------------------

type OpTxSetIPAddress struct {
	OpBase    `yaml:",inline"`
	IPAddress string         `json:"ip_address"`
	Signer    sdk.AccAddress `json:"signer"`
	Sequence  *int64         `json:"sequence"`
	Gas       *int64         `json:"gas"`
}

func (op *OpTxSetIPAddress) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgSetIPAddress(op.IPAddress, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxSolvency ------------------------------

type OpTxSolvency struct {
	OpBase   `yaml:",inline"`
	Chain    common.Chain   `json:"chain"`
	PubKey   common.PubKey  `json:"pub_key"`
	Coins    common.Coins   `json:"coins"`
	Height   int64          `json:"height"`
	Signer   sdk.AccAddress `json:"signer"`
	Sequence *int64         `json:"sequence"`
	Gas      *int64         `json:"gas"`
}

func (op *OpTxSolvency) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg, err := types.NewMsgSolvency(op.Chain, op.PubKey, op.Coins, op.Height, op.Signer)
	if err != nil {
		return err
	}
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxSetNodeKeys ------------------------------

type OpTxSetNodeKeys struct {
	OpBase              `yaml:",inline"`
	PubKeySet           common.PubKeySet `json:"pub_key_set"`
	ValidatorConsPubKey string           `json:"validator_cons_pub_key"`
	Signer              sdk.AccAddress   `json:"signer"`
	Sequence            *int64           `json:"sequence"`
	Gas                 *int64           `json:"gas"`
}

func (op *OpTxSetNodeKeys) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg := types.NewMsgSetNodeKeys(op.PubKeySet, op.ValidatorConsPubKey, op.Signer)
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxTssKeysign ------------------------------

type OpTxTssKeysign struct {
	OpBase                  `yaml:",inline"`
	types.MsgTssKeysignFail `yaml:",inline"`
	// Signer                  sdk.AccAddress `json:"signer"`
	Sequence *int64 `json:"sequence"`
	Gas      *int64 `json:"gas"`
}

func (op *OpTxTssKeysign) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgTssKeysignFail, op.MsgTssKeysignFail.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxTssPool ------------------------------

type OpTxTssPool struct {
	OpBase               `yaml:",inline"`
	PubKeys              []string         `json:"pub_keys"`
	PoolPubKey           common.PubKey    `json:"pool_pub_key"`
	Secp256K1Signature   []byte           `json:"secp256k1_signature"`
	KeysharesBackup      []byte           `json:"keyshares_backup"`
	KeygenType           types.KeygenType `json:"keygen_type"`
	Height               int64            `json:"height"`
	Blame                []types.Blame    `json:"blame"`
	Chains               []string         `json:"chains"`
	Signer               sdk.AccAddress   `json:"signer"`
	KeygenTime           int64            `json:"keygen_time"`
	Sequence             *int64           `json:"sequence"`
	Gas                  *int64           `json:"gas"`
	PoolPubKeyEddsa      common.PubKey    `json:"pool_pub_key_eddsa"`
	KeysharesBackupEddsa []byte           `json:"keyshares_backup_eddsa"`
	KeysharesBackupFrost []byte           `json:"keyshares_backup_frost"`
	PoolPubKeyFrost      common.PubKey    `json:"pool_pub_key_frost"`
	ObservedMoneroHeight int64            `json:"observed_monero_height"`
}

func (op *OpTxTssPool) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	msg, err := types.NewMsgTssPoolV3(op.PubKeys, op.PoolPubKey, op.Secp256K1Signature, op.KeysharesBackup, op.KeygenType, op.Height, op.Blame, op.Chains, op.Signer, op.KeygenTime, op.PoolPubKeyEddsa, op.KeysharesBackupEddsa, op.KeysharesBackupFrost, op.PoolPubKeyFrost, op.ObservedMoneroHeight)
	if err != nil {
		return err
	}
	return sendMsg(out, routine, msg, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxDeposit ------------------------------

type OpTxDeposit struct {
	OpBase           `yaml:",inline"`
	types.MsgDeposit `yaml:",inline"`
	Sequence         *int64 `json:"sequence"`
	Gas              *int64 `json:"gas"`
}

func (op *OpTxDeposit) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgDeposit, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxMimir ------------------------------

type OpTxMimir struct {
	OpBase         `yaml:",inline"`
	types.MsgMimir `yaml:",inline"`
	Sequence       *int64 `json:"sequence"`
	Gas            *int64 `json:"gas"`
}

func (op *OpTxMimir) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgMimir, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxSend ------------------------------

type OpTxSend struct {
	OpBase        `yaml:",inline"`
	types.MsgSend `yaml:",inline"`
	Sequence      *int64 `json:"sequence"`
	Gas           *int64 `json:"gas"`
	Memo          string `json:"memo"`
}

func (op *OpTxSend) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgSend, op.FromAddress, op.Memo, op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxVersion ------------------------------

type OpTxVersion struct {
	OpBase              `yaml:",inline"`
	types.MsgSetVersion `yaml:",inline"`
	Sequence            *int64 `json:"sequence"`
	Gas                 *int64 `json:"gas"`
}

func (op *OpTxVersion) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgSetVersion, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxProposeUpgrade ------------------------------

type OpTxProposeUpgrade struct {
	OpBase                  `yaml:",inline"`
	types.MsgProposeUpgrade `yaml:",inline"`
	Sequence                *int64 `json:"sequence"`
	Gas                     *int64 `json:"gas"`
}

func (op *OpTxProposeUpgrade) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgProposeUpgrade, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxApproveUpgrade ------------------------------

type OpTxApproveUpgrade struct {
	OpBase                  `yaml:",inline"`
	types.MsgApproveUpgrade `yaml:",inline"`
	Sequence                *int64 `json:"sequence"`
	Gas                     *int64 `json:"gas"`
}

func (op *OpTxApproveUpgrade) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgApproveUpgrade, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxRejectUpgrade ------------------------------

type OpTxRejectUpgrade struct {
	OpBase                 `yaml:",inline"`
	types.MsgRejectUpgrade `yaml:",inline"`
	Sequence               *int64 `json:"sequence"`
	Gas                    *int64 `json:"gas"`
}

func (op *OpTxRejectUpgrade) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgRejectUpgrade, op.Signer, "", op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpTxBankSend ------------------------------

type OpTxBankSend struct {
	OpBase            `yaml:",inline"`
	banktypes.MsgSend `yaml:",inline"`
	Sequence          *int64 `json:"sequence"`
	Gas               *int64 `json:"gas"`
	Memo              string `json:"memo"`
}

func (op *OpTxBankSend) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	accAddr, err := sdk.AccAddressFromBech32(op.FromAddress)
	if err != nil {
		return err
	}
	return sendMsg(out, routine, &op.MsgSend, accAddr, op.Memo, op.Sequence, op.Gas, op, "", logs)
}

// ------------------------------ OpFailExportInvariants ------------------------------

type OpFailExportInvariants struct {
	OpBase `yaml:",inline"`
}

func (op *OpFailExportInvariants) Execute(out io.Writer, _ string, _, _ int, _ *os.Process, _ chan string) error {
	return fmt.Errorf("fail-export-invariants should only be the last operation")
}

// ------------------------------ OpTxExecuteContract ------------------------------

type OpTxExecuteContract struct {
	OpBase                       `yaml:",inline"`
	wasmtypes.MsgExecuteContract `yaml:",inline"`
	Sequence                     *int64 `json:"sequence"`
	Gas                          *int64 `json:"gas"`
	Fees                         string `json:"fees"`
}

func (op *OpTxExecuteContract) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgExecuteContract, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

// ------------------------------ OpTxInstantiateContract2 ------------------------------

type OpTxInstantiateContract2 struct {
	OpBase                            `yaml:",inline"`
	wasmtypes.MsgInstantiateContract2 `yaml:",inline"`
	Sequence                          *int64 `json:"sequence"`
	Gas                               *int64 `json:"gas"`
	Fees                              string `json:"fees"`
}

func (op *OpTxInstantiateContract2) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgInstantiateContract2, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

// ------------------------------ OpTxInstantiateContract ------------------------------

type OpTxInstantiateContract struct {
	OpBase                           `yaml:",inline"`
	wasmtypes.MsgInstantiateContract `yaml:",inline"`
	Sequence                         *int64 `json:"sequence"`
	Gas                              *int64 `json:"gas"`
	Fees                             string `json:"fees"`
}

func (op *OpTxInstantiateContract) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgInstantiateContract, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

// ------------------------------ OpTxMigrateContract ------------------------------

type OpTxMigrateContract struct {
	OpBase                       `yaml:",inline"`
	wasmtypes.MsgMigrateContract `yaml:",inline"`
	Sequence                     *int64 `json:"sequence"`
	Gas                          *int64 `json:"gas"`
	Fees                         string `json:"fees"`
}

func (op *OpTxMigrateContract) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgMigrateContract, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

// ------------------------------ OpTxStoreCode ------------------------------

type OpTxStoreCode struct {
	OpBase                 `yaml:",inline"`
	wasmtypes.MsgStoreCode `yaml:",inline"`
	WasmFile               string `json:"wasm_file"`
	Sequence               *int64 `json:"sequence"`
	Gas                    *int64 `json:"gas"`
	Fees                   string `json:"fees"`
}

func (op *OpTxStoreCode) Execute(out io.Writer, path string, _, routine int, _ *os.Process, logs chan string) error {
	wasmPath := filepath.Join("../fixtures/wasm", op.WasmFile)
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}

	// gzip the wasm file
	if ioutils.IsWasm(wasm) {
		wasm, err = ioutils.GzipIt(wasm)
		if err != nil {
			return err
		}
	} else if !ioutils.IsGzip(wasm) {
		return fmt.Errorf("invalid input file. Use wasm binary or gzip")
	}

	op.MsgStoreCode.WASMByteCode = wasm

	return sendMsg(out, routine, &op.MsgStoreCode, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

// ------------------------------ OpTxSudoContract ------------------------------

type OpTxSudoContract struct {
	OpBase                    `yaml:",inline"`
	wasmtypes.MsgSudoContract `yaml:",inline"`
	Sequence                  *int64 `json:"sequence"`
	Gas                       *int64 `json:"gas"`
	Fees                      string `json:"fees"`
}

func (op *OpTxSudoContract) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgSudoContract, sdk.MustAccAddressFromBech32(op.Authority), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

type OpTxUpdateAdmin struct {
	OpBase                   `yaml:",inline"`
	wasmtypes.MsgUpdateAdmin `yaml:",inline"`
	Sequence                 *int64 `json:"sequence"`
	Gas                      *int64 `json:"gas"`
	Fees                     string `json:"fees"`
}

func (op *OpTxUpdateAdmin) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgUpdateAdmin, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

type OpTxClearAdmin struct {
	OpBase                  `yaml:",inline"`
	wasmtypes.MsgClearAdmin `yaml:",inline"`
	Sequence                *int64 `json:"sequence"`
	Gas                     *int64 `json:"gas"`
	Fees                    string `json:"fees"`
}

func (op *OpTxClearAdmin) Execute(out io.Writer, _ string, _, routine int, _ *os.Process, logs chan string) error {
	return sendMsg(out, routine, &op.MsgClearAdmin, sdk.MustAccAddressFromBech32(op.Sender), "", op.Sequence, op.Gas, op, op.Fees, logs)
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func sendMsg(out io.Writer, routine int, msg sdk.Msg, signer sdk.AccAddress, txMemo string, seq, gas *int64, op any, fees string, logs chan string) error {
	log := log.Output(zerolog.ConsoleWriter{Out: out})

	// sdk v50 makes ValidateBasic optional
	msgV, ok := msg.(sdk.HasValidateBasic)
	if ok {
		err := msgV.ValidateBasic()
		if err != nil {
			enc := json.NewEncoder(out) // json instead of yaml to encode amount
			enc.SetIndent("", "  ")
			_ = enc.Encode(op)
			return fmt.Errorf("failed to validate basic: %w", err)
		}
	}

	clientCtx, txFactory := clientContextAndFactory(routine)

	// custom client context
	buf := bytes.NewBuffer(nil)
	ctx := clientCtx.WithFromAddress(signer)
	ctx = ctx.WithFromName(addressToName[signer.String()])
	ctx = ctx.WithOutput(buf)

	// override the sequence if provided
	txf := txFactory
	if seq != nil {
		txf = txf.WithSequence(uint64(*seq))
	}

	// Fees must be set before Gas, or Gas is reset
	if fees != "" {
		txf = txFactory.WithFees(fees)
	}

	// override the cosmos gas if provided
	if gas != nil {
		txf = txf.WithGas(uint64(*gas))
	}

	if txMemo != "" {
		txf = txFactory.WithMemo(txMemo)
	}

	// send message
	if err := tx.GenerateOrBroadcastTxWithFactory(ctx, txf, msg); err != nil {
		_, _ = out.Write([]byte(ColorPurple + "\nOperation:" + ColorReset))
		enc := json.NewEncoder(out) // json instead of yaml to encode amount
		enc.SetIndent("", "  ")
		_ = enc.Encode(op)
		_, _ = out.Write([]byte(ColorPurple + "\nTx Output:" + ColorReset))
		drainLogs(logs)
		return err
	}

	res := buf.Bytes()

	// extract txhash from output json
	var txRes sdk.TxResponse
	if err := encodingConfig.Codec.UnmarshalJSON(res, &txRes); err != nil {
		return fmt.Errorf("failed to unmarshal tx response: %w", err)
	}

	log.Debug().Str("tx", string(res)).Msg("tx sent")

	// fail if tx did not send, otherwise add to out native tx ids
	if txRes.Code != 0 {
		log.Debug().Uint32("code", txRes.Code).Str("log", txRes.RawLog).Msg("tx send failed")
	} else {
		nativeTxIDsMu.Lock()
		nativeTxIDs[routine] = append(nativeTxIDs[routine], txRes.TxHash)
		nativeTxIDsMu.Unlock()
	}

	return nil
}

// recordBlockTime appends "suite,height,microseconds" to REGTEST_BLOCK_TIMES when
// set, for perf analysis of per-block processing time (see scripts/perf).
func recordBlockTime(suite string, height int, elapsed time.Duration) {
	path := os.Getenv("REGTEST_BLOCK_TIMES")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s,%d,%d\n", suite, height, elapsed.Microseconds())
}
