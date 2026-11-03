package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type zecEntrypointFixture struct {
	t    *testing.T
	dir  string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func newZecEntrypointFixture(t *testing.T, mode string, height int) *zecEntrypointFixture {
	t.Helper()
	f := &zecEntrypointFixture{t: t, dir: t.TempDir(), done: make(chan struct{})}
	// The daemon, rather than curl, owns a submitted mining job. Losing the
	// client does not complete that job, and getblockcount remains responsive.
	f.write("zebrad", `#!/bin/sh
echo $$ >"$FIXTURE/daemon-pid"
trap 'touch "$FIXTURE/daemon-stopped"' EXIT
trap 'exit 0' TERM
while [ ! -e "$FIXTURE/stop" ]; do
  if [ -e "$FIXTURE/server-request" ]; then
    touch "$FIXTURE/server-active"
    if [ -e "$FIXTURE/server-release" ]; then
      touch "$FIXTURE/server-completed"
    fi
  fi
  if [ -e "$FIXTURE/daemon-failure" ]; then exit 23; fi
  /bin/sleep 0.01
done
`)
	f.write("curl", `#!/bin/sh
out= writeout= data= timeout= connect_timeout=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2;;
    -w) writeout=$2; shift 2;;
    -d) data=$2; shift 2;;
    --max-time) timeout=$2; shift 2;;
    --connect-timeout) connect_timeout=$2; shift 2;;
    *) shift;;
  esac
done
case "$data" in
  *getblockcount*) printf '{"result":%s,"error":null}\n' "$START_HEIGHT"; exit 0;;
  *'"generate"'*) ;;
  *) exit 2;;
esac
if [ -z "$out" ] || [ "$writeout" != '%{size_request}' ]; then
  echo 'curl fixture requires -o and -w size_request' >&2
  exit 2
fi
printf '%s %s\n' "$timeout" "$connect_timeout" >"$FIXTURE/timeouts"
count=0
if [ -e "$FIXTURE/count" ]; then count=$(cat "$FIXTURE/count"); fi
count=$((count + 1))
echo "$count" >"$FIXTURE/count"
echo "generate-$count" >>"$FIXTURE/events"
if [ -e "$FIXTURE/server-active" ] && [ ! -e "$FIXTURE/server-completed" ]; then
  touch "$FIXTURE/overlap"
fi
rc=0
sent=123
body='{"result":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"error":null}'
if [ "$count" = 1 ]; then
  case "$MODE" in
    slow-success|sent-timeout|missing-stats|malformed-stats|killed-client)
      touch "$FIXTURE/server-request"
      while [ ! -e "$FIXTURE/server-active" ]; do /bin/sleep 0.01; done
      if [ "$MODE" = slow-success ]; then
        while [ ! -e "$FIXTURE/server-completed" ]; do /bin/sleep 0.01; done
      else
        rc=28
        body=
      fi;;
  esac
  case "$MODE" in
    connection-refused) rc=7; sent=0; body=;;
    connection-timeout) rc=28; sent=0; body=;;
    missing-stats) sent=;;
    malformed-stats) sent=unknown;;
    killed-client)
      touch "$FIXTURE/client-returned"
      kill -KILL $$;;
    rpc-error) body='{"result":null,"error":{"code":-1,"message":"generate failed"}}';;
    http-error) body='<html>503 unavailable</html>';;
    bad-result) body='{"result":[],"error":null}';;
  esac
fi
printf '%s' "$body" >"$out"
printf '%s' "$sent"
touch "$FIXTURE/client-returned"
exit "$rc"
`)
	f.write("sleep", `#!/bin/sh
case "$1" in
  7)
    echo paced >>"$FIXTURE/events"
    touch "$FIXTURE/stop"
    while [ ! -e "$FIXTURE/daemon-stopped" ]; do /bin/sleep 0.01; done
    /bin/sleep 0.05;;
  1)
    echo retry >>"$FIXTURE/events"
    /bin/sleep 0.01;;
  *)
    echo "sleep-$1" >>"$FIXTURE/events"
    /bin/sleep 0.01;;
esac
`)
	f.cmd = exec.Command("sh", filepath.Join(repoRoot(t), "build/docker/mocknet/zcash/entrypoint.sh"))
	f.cmd.Env = append(os.Environ(), "PATH="+f.dir+":"+os.Getenv("PATH"), "FIXTURE="+f.dir,
		"MODE="+mode, "START_HEIGHT="+strconv.Itoa(height), "BLOCK_TIME=7", "BOOTSTRAP_TIMEOUT=5")
	output, err := os.Create(filepath.Join(f.dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	f.cmd.Stdout, f.cmd.Stderr = output, output
	if err := f.cmd.Start(); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	go func() { f.err = f.cmd.Wait(); close(f.done) }()
	t.Cleanup(func() {
		f.write("server-release", "")
		f.write("stop", "")
		select {
		case <-f.done:
		case <-time.After(3 * time.Second):
			_ = f.cmd.Process.Kill()
			<-f.done
		}
		_ = output.Close()
	})
	return f
}

func (f *zecEntrypointFixture) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func (f *zecEntrypointFixture) read(name string) string {
	data, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(data)
}

func (f *zecEntrypointFixture) exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}

func (f *zecEntrypointFixture) waitFor(check func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timeout: events=%s output=%s", f.read("events"), f.read("output"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *zecEntrypointFixture) waitForExit(success bool) {
	f.t.Helper()
	select {
	case <-f.done:
		if (f.err == nil) != success {
			f.t.Fatalf("exit=%v output=%s", f.err, f.read("output"))
		}
	case <-time.After(30 * time.Second):
		f.t.Fatalf("entrypoint did not exit: events=%s output=%s", f.read("events"), f.read("output"))
	}
}

func TestZecEntrypointWarmupUsesExistingHeight(t *testing.T) {
	for _, tc := range []struct {
		name               string
		height, wantBlocks int
	}{
		{name: "fresh", height: 0, wantBlocks: 400},
		{name: "restored", height: 805, wantBlocks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newZecEntrypointFixture(t, "success", tc.height)
			f.waitForExit(true)
			if got := strings.TrimSpace(f.read("count")); got != strconv.Itoa(tc.wantBlocks) {
				t.Fatalf("mined %s blocks before pacing, want %d", got, tc.wantBlocks)
			}
			if !strings.HasSuffix(f.read("events"), "paced\n") {
				t.Fatalf("missing normal block pacing: %s", f.read("events"))
			}
			if got := f.read("timeouts"); got != "600 10\n" {
				t.Fatalf("mining/connect timeout defaults=%q", got)
			}
		})
	}
}

func TestZecEntrypointWaitsForSlowSuccessfulMining(t *testing.T) {
	f := newZecEntrypointFixture(t, "slow-success", 399)
	f.waitFor(func() bool { return f.exists("server-active") })
	// The accepted request is still running inside the daemon.
	time.Sleep(100 * time.Millisecond)
	if f.read("count") != "1\n" || f.exists("client-returned") || f.exists("overlap") {
		t.Fatalf("mining advanced before completion: events=%s", f.read("events"))
	}
	f.write("server-release", "")
	f.waitForExit(true)
	if f.read("count") != "1\n" || !strings.Contains(f.read("events"), "paced\n") {
		t.Fatalf("successful mining did not resume normal pacing: %s", f.read("events"))
	}
}

func TestZecEntrypointRetriesOnlyConfirmedUnsentRequests(t *testing.T) {
	for _, mode := range []string{"connection-refused", "connection-timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newZecEntrypointFixture(t, mode, 805)
			f.waitForExit(true)
			if got := f.read("events"); got != "generate-1\nretry\ngenerate-2\npaced\n" {
				t.Fatalf("wrong retry sequence: %s", got)
			}
		})
	}
}

func TestZecEntrypointPreservesDaemonAfterAmbiguousMining(t *testing.T) {
	for _, mode := range []string{"sent-timeout", "missing-stats", "malformed-stats", "killed-client", "rpc-error", "http-error", "bad-result"} {
		t.Run(mode, func(t *testing.T) {
			f := newZecEntrypointFixture(t, mode, 805)
			f.waitFor(func() bool {
				return f.exists("client-returned") && strings.Contains(f.read("output"), "stopping mining")
			})
			if f.exists("server-active") {
				// A healthy getblockcount response does not finish this independent job.
				probe := exec.Command(filepath.Join(f.dir, "curl"), "-d", `{"method":"getblockcount"}`)
				probe.Env = f.cmd.Env
				if out, err := probe.Output(); err != nil || !strings.Contains(string(out), `"result":805`) {
					t.Fatalf("daemon readiness probe: %q %v", out, err)
				}
				if f.exists("server-completed") {
					t.Fatal("server job ended with its client")
				}
				f.write("server-release", "")
				f.waitFor(func() bool { return f.exists("server-completed") })
			}
			time.Sleep(100 * time.Millisecond)
			if f.read("count") != "1\n" || f.exists("overlap") || f.exists("daemon-stopped") {
				t.Fatalf("ambiguous request retried or daemon stopped: events=%s output=%s", f.read("events"), f.read("output"))
			}
			select {
			case <-f.done:
				t.Fatalf("entrypoint exited while daemon was alive: %v", f.err)
			default:
			}
			// Leaving the daemon alive must not hide its eventual failure.
			f.write("daemon-failure", "")
			f.waitForExit(false)
		})
	}
}
