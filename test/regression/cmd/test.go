package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/rs/zerolog/log"
)

type regressionTest struct {
	// test state
	files []string // list of test files to run
	total int      // total number of tests to run

	inProgress []string // list of test files in progress
	succeeded  []string // list of test files that succeeded
	failed     []string // list of test files that failed
	completed  int      // number of tests completed

	// concurrency control
	parallelism  int            // number of tests to run in parallel
	sem          chan struct{}  // semaphore to control parallelism
	abort        chan struct{}  // abort early if there is a failure in a merge request run
	retryCount   map[string]int // number of retries for each test
	retryCountMu sync.Mutex
	mu           sync.Mutex     // mutex to protect shared state
	wg           sync.WaitGroup // wait group to wait for all tests to finish
}

func newRegressionTest(files []string, parallelism int) *regressionTest {
	total := len(files)
	if parallelism > total {
		parallelism = total
	}
	return &regressionTest{
		files:       files,
		total:       total,
		sem:         make(chan struct{}, parallelism),
		abort:       make(chan struct{}),
		retryCount:  make(map[string]int),
		parallelism: parallelism,
	}
}

func (r *regressionTest) run() {
	log.Info().
		Int("parallelism", r.parallelism).
		Int("count", r.total).
		Msg("running tests")
	fmt.Println() // A blank line before the first regression test whether parallel or not.

	// run tests
TestLoop:
	for i, file := range r.files {
		select {
		case r.sem <- struct{}{}:
		case <-r.abort:
			// break if aborted
			break TestLoop
		}

		r.wg.Add(1)
		r.markStart(file, i)
		go r.runTestFile(file, i)
	}

	// wait for all tests to finish
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	// wait for all tests to finish or abort
	select {
	case <-done:
	case <-r.abort:
		fmt.Printf("%s>> FAIL_FAST: Aborting Now <<%s\n", ColorRed, ColorReset)
	}

	r.printResults(r.retryCount)

	// exit with error code if any tests failed
	if len(r.failed) > 0 {
		os.Exit(1)
	}
}

func (r *regressionTest) runTestFile(file string, i int) {
	// create home directory
	home := regtestHome(i)
	_ = os.MkdirAll(home, 0o755)

	// create a buffer to capture the logs
	var out io.Writer = os.Stderr
	buf := new(bytes.Buffer)
	if r.parallelism > 1 {
		out = buf
	}

	success := false
	defer func() {
		r.markDone(file, buf, success)
	}()

	// run test
	failExportInvariants, runErr := run(out, file, i, r.doneWithRetries, r.abort)
	if runErr != nil {
		return
	}

	// check export state
	exportErr := export(out, file, i, failExportInvariants)
	if exportErr != nil {
		return
	}

	success = true
}

func (r *regressionTest) markStart(file string, i int) {
	log.Info().Str("test", file).Msg("Starting")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inProgress = append(r.inProgress, file)
	if i >= r.parallelism {
		r.printStatus()
	}
}

func (r *regressionTest) markDone(file string, buf *bytes.Buffer, success bool) {
	r.mu.Lock()
	defer func() {
		r.mu.Unlock()

		// release semaphore and wait group
		r.wg.Done()
		<-r.sem
	}()

	if success {
		r.succeeded = append(r.succeeded, file)
	} else {
		r.failed = append(r.failed, file)
		if os.Getenv("FAIL_FAST") != "" {
			close(r.abort)
		}
	}

	r.completed++
	if r.parallelism > 1 {
		fmt.Print(buf.String())
		fmt.Println() // Blank line separating regression tests.

	}

	for i, f := range r.inProgress {
		if f == file {
			r.inProgress = append(r.inProgress[:i], r.inProgress[i+1:]...)
			break
		}
	}
	if r.total-r.completed < r.parallelism && len(r.inProgress) > 0 {
		r.printStatus()
	}
}

func (r *regressionTest) printStatus() {
	log.Info().Str("completed", fmt.Sprintf("%d/%d", r.completed, r.total)).Strs("failed", r.failed).Strs("in_progress", r.inProgress).Msg("Status")
	fmt.Println() // Blank line separating regression tests.
}

func (r *regressionTest) printResults(retries map[string]int) {
	// print the results
	fmt.Printf("%sSucceeded:%s %d\n", ColorGreen, ColorReset, len(r.succeeded))
	for _, file := range r.succeeded {
		fmt.Printf("- %s\n", file)
	}
	fmt.Printf("%sRetried:%s %d\n", ColorYellow, ColorReset, len(retries))
	for file, count := range retries {
		fmt.Printf("- %s: %d\n", file, count)
	}
	fmt.Printf("%sFailed:%s %d\n", ColorRed, ColorReset, len(r.failed))
	for _, file := range r.failed {
		fmt.Printf("- %s\n", file)
	}
	fmt.Println()
}

func (r *regressionTest) doneWithRetries(file string) bool {
	r.retryCountMu.Lock()
	defer r.retryCountMu.Unlock()
	r.retryCount[file]++
	return r.retryCount[file] >= 3
}
