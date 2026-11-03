package thorchain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	thorlog "gitlab.com/thorchain/thornode/v3/log"
)

// RegisterLogsRoute registers the /thorchain/logs handler.
//
// Deliberately a plain HTTP route rather than a gRPC query: the sdk's local
// ABCI client shares one mutex across all four connections, so an ABCI query
// serializes with FinalizeBlock and a multi-second log scan would stall block
// execution. These files are node-local, never consensus state, so nothing
// here needs the ABCI path at all. Serving it directly also gives the read a
// request context to cancel on.
func RegisterLogsRoute(router *mux.Router) {
	router.HandleFunc("/thorchain/logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Request validation lives here in full: the log package is a store.
		start, err := heightParam(q.Get("start"))
		if err != nil {
			http.Error(w, "start: "+err.Error(), http.StatusBadRequest)
			return
		}
		end, err := heightParam(q.Get("end"))
		if err != nil {
			http.Error(w, "end: "+err.Error(), http.StatusBadRequest)
			return
		}
		level := q.Get("level")
		if level == "" {
			level = "info"
		}
		modules, all := modulesParam(q["modules"])
		if !all && len(modules) == 0 {
			modules = []string{thorlog.DefaultModule}
		}
		content, err := thorlog.ReadBlockLogs(r.Context(), start, end, level, modules)
		if err != nil {
			status, ok := logsErrorStatus(err)
			if !ok {
				return // client hung up, nothing to write back to
			}
			if status == http.StatusServiceUnavailable {
				w.Header().Set("Retry-After", "1")
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(content)
	})
}

// modulesParam reads the repeated modules parameter. Empty values are dropped
// rather than matched: `?modules=` would otherwise select the module named ""
// and, since every stored entry carries a module, hand back an empty array for
// what is almost always a typo. "*" asks for every module, the one way to
// reach ReadBlockLogs' match-all and the only way to chase a problem across
// modules without naming each sdk module up front.
func modulesParam(raw []string) (modules []string, all bool) {
	for _, m := range raw {
		switch m {
		case "":
		case "*":
			return nil, true
		default:
			modules = append(modules, m)
		}
	}
	return modules, false
}

// heightParam parses an optional block height. Empty means "unspecified",
// which ReadBlockLogs resolves to the last captured block.
func heightParam(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	h, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid height param, %w", err)
	}
	if h < 1 {
		return 0, fmt.Errorf("invalid height param, must be positive")
	}
	return h, nil
}

// logsErrorStatus maps a read error to its status. Reports false when there is
// nobody left to answer.
//
// The two node-side failures are kept off 4xx so an operator watching error
// rates on the api port can still tell a disk problem from a client typo: a
// saturated read is a 503, and a block file that would not read is a 500.
// Everything else ReadBlockLogs rejects is the caller's to fix. Capture being
// disabled stays a 400 by decision, not oversight: it is the documented
// contract the health skill reads to tell "this node has the endpoint turned
// off" from "this node is broken".
func logsErrorStatus(err error) (int, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return 0, false
	case errors.Is(err, thorlog.ErrTooManyReads):
		return http.StatusServiceUnavailable, true
	case errors.Is(err, thorlog.ErrBlockRead):
		return http.StatusInternalServerError, true
	default:
		return http.StatusBadRequest, true
	}
}
