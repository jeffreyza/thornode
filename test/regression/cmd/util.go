package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

func regtestMntDir() string {
	if dir := os.Getenv("REGTEST_MNT_DIR"); dir != "" {
		return dir
	}
	return "/mnt"
}

func regtestCreateBlockPort(routine int) int {
	base := 8080
	if val := os.Getenv("REGTEST_CREATE_BLOCK_PORT_BASE"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			base = parsed
		}
	}
	return base + routine
}

// regtestEbifrostPort keeps the per-routine ebifrost listener below Linux's
// default ephemeral range (32768-60999). Inside it, an outbound localhost
// connection from a parallel routine can already own the port as its source
// port, and thornode then panics with "bind: address already in use".
func regtestEbifrostPort(routine int) int {
	return 30000 + routine
}

func regtestHome(routine int) string {
	base := os.Getenv("REGTEST_HOME_BASE")
	if base == "" {
		base = "/"
	}
	return filepath.Join(base, strconv.Itoa(routine))
}

// deepMerge merges two maps recursively - if there is an array in the map it will deep
// merge elements that match on keys in overrideKeys.
func deepMerge(a, b map[string]any, overrideKeys ...string) map[string]any {
	result := make(map[string]any)
	for k, v := range a {
		result[k] = v
	}

	for k := range b {
		switch v := b[k].(type) {
		case []any:
			if avx, ok := result[k]; ok {
				var av []any
				if av, ok = avx.([]any); ok {

					// deep merge if key in overrideKeys matches in the source and destination
					for _, ov := range overrideKeys {
						for i := range av {
							avEntry, ok := av[i].(map[string]any)
							if !ok {
								continue
							}
							for j := range v {
								vEntry, ok := v[j].(map[string]any)
								if !ok {
									continue
								}
								if _, ok = avEntry[ov]; !ok {
									continue
								}
								if _, ok = vEntry[ov]; !ok {
									continue
								}

								if avEntry[ov] == vEntry[ov] {
									av[i] = deepMerge(avEntry, vEntry, overrideKeys...)

									// remove value from v
									v = append(v[:j], v[j+1:]...)
									break
								}
							}
						}
					}

					result[k] = append(av, v...)
					continue
				}
			}
		case map[string]any:
			if avx, ok := result[k]; ok {
				var av map[string]any
				if av, ok = avx.(map[string]any); ok {
					result[k] = deepMerge(av, v, overrideKeys...)
					continue
				}
			}
		}
		result[k] = b[k]
	}
	return result
}

func dumpLogs(out io.Writer, logs chan string) {
	for {
		select {
		case line := <-logs:
			_, _ = out.Write([]byte(line + "\n"))
			continue
		default:
		}
		break
	}
}

func drainLogs(logs chan string) {
	// if DEBUG is set skip draining logs
	if os.Getenv("DEBUG") != "" {
		return
	}

	for {
		select {
		case <-logs:
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
}

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(os.Signal(nil))
	return err == nil
}

func getTimeFactor() time.Duration {
	tf, err := strconv.ParseInt(os.Getenv("TIME_FACTOR"), 10, 64)
	if err != nil {
		return time.Duration(1)
	}
	return time.Duration(tf)
}

func consoleLogger(w io.Writer) zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{Out: w}).With().Timestamp().Caller().Logger()
}

var reSetVar = regexp.MustCompile(`\$\{([A-Z0-9_]+)=([^}]+)\}`)

func parseOps(localLog zerolog.Logger, path string, tmpls *template.Template, env []string) (ops []Operation, opLines []int, newEnv []string, failExportInvariants bool) {
	// read the file
	f, err := os.Open(path)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open test file")
	}
	fileBytes, err := io.ReadAll(f)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to read test file")
	}
	f.Close()

	// track line numbers
	opLines = []int{0}
	scanner := bufio.NewScanner(bytes.NewBuffer(fileBytes))
	for i := 0; scanner.Scan(); i++ {
		line := scanner.Text()
		if line == "---" {
			opLines = append(opLines, i+2)
		}
	}

	// parse the template
	tmpl, err := tmpls.Parse(string(fileBytes))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse template")
	}

	// render the template
	tmplBuf := &bytes.Buffer{}
	err = tmpl.Execute(tmplBuf, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to render template")
	}

	// scan all lines in buffer for variable expansion
	buf := &bytes.Buffer{}
	scanner = bufio.NewScanner(tmplBuf)
	vars := map[string]string{}
	for i := 0; scanner.Scan(); i++ {
		line := scanner.Text()
		matches := reSetVar.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			vars[match[1]] = match[2]
		}

		// regex replace variables and set variables
		for k, v := range vars {
			re := regexp.MustCompile(`\$\{` + k + `(=([^}]+))?\}`)
			line = re.ReplaceAllString(line, v)
		}

		// write line
		buf.WriteString(line + "\n")
	}

	// track whether we complete seen state and env operations
	stateComplete := false
	envComplete := false
	envAdded := 0

	dec := yaml.NewDecoder(buf)
	for i := 0; ; i++ {
		// decode into temporary type
		op := map[string]any{}
		err = dec.Decode(&op)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatal().Str("path", path).Err(err).Msg("failed to decode operation")
		}

		// warn empty operations
		if len(op) == 0 {
			log.Fatal().Str("path", path).Int("line", opLines[i]).Msg("empty operation not allowed")
			continue
		}

		// env operations are special
		if op["type"] == "env" {
			if envComplete {
				log.Fatal().Msg("env operations must be first")
			}
			o, err := NewOperation(op)
			if err != nil {
				log.Fatal().Str("path", path).Err(err).Msg("failed to create operation")
			}
			envOp, ok := o.(*OpEnv)
			if !ok {
				log.Fatal().Str("path", path).Msg("env operation has unexpected type")
			}
			env = append(env, fmt.Sprintf("%s=%s", envOp.Key, envOp.Value))
			envAdded++
			continue
		}
		envComplete = true

		// state operations must be first
		if op["type"] == "state" && stateComplete {
			log.Fatal().Msg("state operations must come before all operations other than env")
		}
		if op["type"] != "state" {
			stateComplete = true
		}

		operation, err := NewOperation(op)
		if err != nil {
			log.Fatal().Str("path", path).Int("line", opLines[i]).Err(err).Msg("failed to create operation")
		}
		ops = append(ops, operation)
	}

	// remove env operations from op lines so numbers are correct
	opLines = opLines[envAdded:]

	// extract fail-export operation from end if provided
	if _, ok := ops[len(ops)-1].(*OpFailExportInvariants); ok {
		failExportInvariants = true
		ops = ops[:len(ops)-1]
	}

	return ops, opLines, env, failExportInvariants
}

func blockCount(ops []Operation) int {
	blocks := 0
	for _, op := range ops {
		if createBlocks, ok := op.(*OpCreateBlocks); ok {
			blocks += createBlocks.Count
		}
	}
	return blocks
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return false
}

// cleanExports will remove all block and export files without a corresponding test.
func cleanExports() {
	mntDir := regtestMntDir()
	reBlockExport := regexp.MustCompile(fmt.Sprintf(`^%s/blocks/.*/[0-9]+\.json$`, regexp.QuoteMeta(mntDir)))
	for _, dir := range []string{filepath.Join(mntDir, "exports"), filepath.Join(mntDir, "blocks")} {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			// skip the gitignored single block exports
			if reBlockExport.MatchString(path) {
				return nil
			}

			// check if the test exists
			testPath := path[:len(path)-5]
			testPath = strings.TrimPrefix(testPath, dir)
			testPath = "suites" + testPath + ".yaml"
			if !pathExists(testPath) {
				log.Info().Str("path", path).Msg("removing dead export")
				return os.Remove(path)
			}

			return nil
		})
	}
}
