package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"
)

////////////////////////////////////////////////////////////////////////////////////////
// Run
////////////////////////////////////////////////////////////////////////////////////////

func run(out io.Writer, path string, routine int, doneWithRetries func(path string) bool, abort <-chan struct{}) (failExportInvariants bool, err error) {
	localLog := consoleLogger(out)

	home := regtestHome(routine)
	mntDir := regtestMntDir()
	localLog.Info().Str("path", path).Msgf("Loading regression test")

	// clear data directory
	localLog.Debug().Msg("Clearing data directory")
	thornodePath := filepath.Join(home, ".thornode")
	cmdOut, err := exec.Command("rm", "-rf", thornodePath).CombinedOutput()
	if err != nil {
		fmt.Println(string(cmdOut))
		log.Fatal().Err(err).Msg("failed to clear data directory")
	}

	// use same environment for all commands
	env := []string{
		"HOME=" + home,
		"SIGNER_NAME=thorchain",
		"SIGNER_PASSWD=password",
		"CHAIN_HOME_FOLDER=" + thornodePath,
		"THOR_TENDERMINT_INSTRUMENTATION_PROMETHEUS=false",
		// block time should be short, but all consecutive checks must complete within timeout
		fmt.Sprintf("THOR_TENDERMINT_CONSENSUS_TIMEOUT_COMMIT=%s", time.Second*getTimeFactor()),
		// all ports will be offset by the routine number
		fmt.Sprintf("THOR_COSMOS_API_ADDRESS=tcp://0.0.0.0:%d", 1317+routine),
		fmt.Sprintf("THOR_COSMOS_EBIFROST_ADDRESS=127.0.0.1:%d", regtestEbifrostPort(routine)),
		fmt.Sprintf("THOR_TENDERMINT_RPC_LISTEN_ADDRESS=tcp://0.0.0.0:%d", 26657+routine),
		fmt.Sprintf("THOR_TENDERMINT_P2P_LISTEN_ADDRESS=tcp://0.0.0.0:%d", 27000+routine),
		"CREATE_BLOCK_PORT=" + strconv.Itoa(regtestCreateBlockPort(routine)),
		"GOCOVERDIR=" + filepath.Join(mntDir, "coverage"),
	}

	// opt-in CPU profile of the node under test (see scripts/perf)
	if dir := os.Getenv("REGTEST_CPUPROFILE_DIR"); dir != "" {
		env = append(env, "THORNODE_CPUPROFILE="+filepath.Join(dir, fmt.Sprintf("routine-%d", routine)))
	}

	// if DEBUG is set also output thornode debug logs
	debugVar := os.Getenv("DEBUG")
	if debugVar != "" {
		env = append(env, "THOR_TENDERMINT_LOG_LEVEL="+debugVar)
	}

	// init chain with dog mnemonic
	localLog.Debug().Msg("Initializing chain")
	cmd := exec.Command("thornode", "init", "local", "--chain-id", "thorchain", "--recover")
	cmd.Stdin = bytes.NewBufferString(dogMnemonic + "\n")
	cmd.Env = env
	cmdOut, err = cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(cmdOut))
		log.Fatal().Err(err).Msg("failed to initialize chain")
	}

	// init keys with dog mnemonic
	localLog.Debug().Msg("Initializing keys")
	cmd = exec.Command("thornode", "keys", "--keyring-backend=file", "add", "--recover", "thorchain")
	cmd.Stdin = bytes.NewBufferString(dogMnemonic + "\npassword\npassword\n")
	cmd.Env = env
	cmdOut, err = cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(cmdOut))
		log.Fatal().Err(err).Msg("failed to initialize keys")
	}

	localLog.Debug().Msg("Initializing ed25519 keys")
	cmd = exec.Command("thornode", "ed25519")
	cmd.Stdin = bytes.NewBufferString(dogMnemonic + "\npassword\npassword\n")
	cmd.Env = env
	cmdOut, err = cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(cmdOut))
		log.Fatal().Err(err).Msg("failed to initialize keys")
	}

	// init chain
	localLog.Debug().Msg("Initializing chain")
	cmd = exec.Command("thornode", "init", "local", "--chain-id", "thorchain", "-o")
	cmd.Env = env
	cmdOut, err = cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(cmdOut))
		log.Fatal().Err(err).Msg("failed to initialize chain")
	}

	// create routine local state (used later by custom template functions in operations)
	nativeTxIDsMu.Lock()
	nativeTxIDs[routine] = []string{}
	nativeTxIDsMu.Unlock()
	resetLuviMonitor(routine)
	tmpls := template.Must(templates.Clone())

	// ensure no naming collisions
	if tmpls.Lookup(filepath.Base(path)) != nil {
		log.Fatal().Msgf("test name collision: %s", filepath.Base(path))
	}

	ops, opLines, env, failExportInvariants := parseOps(localLog, path, tmpls, env)

	// warn if no operations found
	if len(ops) == 0 {
		err = errors.New("no operations found")
		localLog.Err(err).Msg("")
		return false, err
	}

	localLog.Info().Str("path", path).Int("blocks", blockCount(ops)).Msgf("Running regression test")

	// clear block directory
	blocksPath := filepath.Join(mntDir, "blocks", strings.TrimPrefix(path, "suites/"))
	blocksPath = strings.TrimSuffix(blocksPath, ".yaml")
	_ = os.RemoveAll(blocksPath)

	// execute all state operations
	stateOpCount := 0
	for i, op := range ops {
		select {
		case <-abort:
			return false, nil
		default:
		}
		if _, ok := op.(*OpState); ok {
			localLog.Info().Int("line", opLines[i]).Msgf(">>> [%d] %s", i+1, op.OpType())
			err = op.Execute(out, path, opLines[i], routine, cmd.Process, nil)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to execute state operation")
			}
			stateOpCount++
		}
	}
	ops = ops[stateOpCount:]
	opLines = opLines[stateOpCount:]

	// validate genesis
	localLog.Debug().Msg("Validating genesis")
	cmd = exec.Command("thornode", "genesis", "validate")
	cmd.Env = env
	cmdOut, err = cmd.CombinedOutput()
	if err != nil {
		cmdErr := err
		// dump the genesis
		fmt.Println(ColorPurple + "Genesis:" + ColorReset)
		var f *os.File
		f, err = os.OpenFile(filepath.Join(home, ".thornode/config/genesis.json"), os.O_RDWR, 0o644)
		if err != nil {
			log.Fatal().Err(err).Str("path", path).Msg("failed to open genesis file")
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fmt.Println(scanner.Text())
		}
		f.Close()

		// dump error and exit
		fmt.Println(string(cmdOut))
		log.Fatal().Err(cmdErr).Msg("genesis validation failed")
	}

	// render config
	localLog.Debug().Msg("Rendering config")
	cmd = exec.Command("thornode", "render-config")
	cmd.Env = env
	err = cmd.Run()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to render config")
	}

	// overwrite private validator key
	localLog.Debug().Msg("Overwriting private validator key")
	keyPath := filepath.Join(home, ".thornode/config/priv_validator_key.json")
	if err = copyFile(filepath.Join(mntDir, "priv_validator_key.json"), keyPath); err != nil {
		log.Fatal().Err(err).Msg("failed to overwrite private validator key")
	}

	logLevel := "info"
	switch debugVar {
	case "trace", "debug", "info", "warn", "error", "fatal", "panic":
		logLevel = debugVar
	}

	regtestBinDir := os.Getenv("REGTEST_BIN_DIR")
	if regtestBinDir == "" {
		regtestBinDir = "/regtest"
	}
	regtestBinDir = filepath.Clean(regtestBinDir)
	if !filepath.IsAbs(regtestBinDir) {
		log.Fatal().Str("path", regtestBinDir).Msg("REGTEST_BIN_DIR must be absolute")
	}
	coverThornodePath := filepath.Join(regtestBinDir, "cover-thornode")

	// setup process io
	// #nosec G204 G702 -- REGTEST_BIN_DIR is provided by the regression test harness.
	thornode := exec.Command(coverThornodePath, "--log_level", logLevel, "start")
	thornode.Env = env

	stderr, err := thornode.StderrPipe()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to setup thornode stderr")
	}
	stderrScanner := bufio.NewScanner(stderr)
	stderrLines := make(chan string, 100)
	go func() {
		for stderrScanner.Scan() {
			stderrLines <- stderrScanner.Text()
		}
	}()
	if debugVar != "" {
		thornode.Stdout = os.Stdout
		thornode.Stderr = os.Stderr
	}

	// start thornode process
	localLog.Debug().Msg("Starting thornode")
	err = thornode.Start()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start thornode")
	}

	// wait for thornode to listen on block creation port
	time.Sleep(time.Second)
	for i := 0; ; i++ {
		if i%100 == 0 {
			localLog.Debug().Msg("Waiting for thornode to listen")
		}
		time.Sleep(100 * time.Millisecond)
		var conn net.Conn
		conn, err = net.Dial("tcp", fmt.Sprintf("localhost:%d", regtestCreateBlockPort(routine)))
		if err == nil {
			conn.Close()
			break
		}
	}

	// run the operations
	var returnErr error
	localLog.Info().Msgf("Executing %d operations", len(ops))
	for i, op := range ops {
		select {
		case <-abort:
			return false, nil
		default:
		}

		// prefetch sequences of check operations
		if os.Getenv("AUTO_UPDATE") == "" && op.OpType() == "check" && ops[i-1].OpType() != "check" {
			wg := sync.WaitGroup{}
			for j := i; j < len(ops); j++ {
				select {
				case <-abort:
					return false, nil
				default:
				}
				if ops[j].OpType() != "check" {
					break
				}
				checkOp, ok := ops[j].(*OpCheck)
				if !ok {
					continue
				}
				wg.Add(1)
				go func(checkOp *OpCheck) {
					defer wg.Done()
					checkOp.prefetch(routine)
				}(checkOp)
			}
			wg.Wait()
		}

		localLog.Info().Int("line", opLines[i]).Msgf(">>> [%d] %s", stateOpCount+i+1, op.OpType())
		returnErr = op.Execute(out, path, opLines[i], routine, thornode.Process, stderrLines)
		if returnErr != nil {
			localLog.Error().Err(returnErr).
				Int("line", opLines[i]).
				Int("op", stateOpCount+i+1).
				Str("type", op.OpType()).
				Str("path", path).
				Msg("operation failed")
			dumpLogs(out, stderrLines)
			break
		}

		// if in AUTO_UPDATE and the operation was a check, re-render future operations
		if os.Getenv("AUTO_UPDATE") != "" && op.OpType() == "check" {
			newOps, _, _, _ := parseOps(localLog, path, tmpls, env)
			remainingOps := len(ops) - i - 1
			newOps = newOps[len(newOps)-remainingOps:]
			for j, newOp := range newOps {
				ops[i+j+1] = newOp
			}
		}
	}

	// log success
	if returnErr == nil {
		localLog.Info().Msg("All operations succeeded")
	}

	// stop thornode process
	localLog.Debug().Msg("Stopping thornode")
	err = thornode.Process.Signal(syscall.SIGUSR1)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to stop thornode")
	}

	// wait for process to exit
	_, err = thornode.Process.Wait()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to wait for thornode")
	}

	// retry context deadline exceeded errors and all errors in CI
	if returnErr != nil && (strings.Contains(returnErr.Error(), "context deadline exceeded") || os.Getenv("CI") != "") {
		log.Warn().Err(returnErr).Str("path", path).Msg("retrying suite")
		fmt.Println()

		if doneWithRetries(path) {
			return false, returnErr
		}

		return run(out, path, routine, doneWithRetries, abort)
	}

	// if failed and debug enabled restart to allow inspection
	if returnErr != nil && os.Getenv("DEBUG") != "" {

		// remove validator key (otherwise thornode will hang in begin block)
		localLog.Debug().Msg("Removing validator key")
		cmd = exec.Command("rm", keyPath)
		cmdOut, err = cmd.CombinedOutput()
		if err != nil {
			fmt.Println(string(cmdOut))
			log.Fatal().Err(err).Msg("failed to remove validator key")
		}

		// restart thornode
		localLog.Debug().Msg("Restarting thornode")
		thornode = exec.Command("thornode", "--log_level", logLevel, "start")
		thornode.Env = env
		thornode.Stdout = os.Stdout
		thornode.Stderr = os.Stderr
		err = thornode.Start()
		if err != nil {
			log.Fatal().Err(err).Msg("failed to restart thornode")
		}

		// if CI is set, don't wait forever
		if os.Getenv("CI") != "" {
			localLog.Debug().Msg("CI mode: exiting without waiting for debug thornode")
		} else {
			// wait for thornode
			localLog.Debug().Msg("Waiting for thornode")
			_, err = thornode.Process.Wait()
			if err != nil {
				log.Fatal().Err(err).Msg("failed to wait for thornode")
			}
		}
	}

	return failExportInvariants, returnErr
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
