package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server"
)

func sdkConsoleLogger(t testing.TB, settings map[string]any, out io.Writer) (*zerolog.Logger, *viper.Viper) {
	v := viper.New()
	for key, value := range settings {
		v.Set(key, value)
	}
	logger, err := server.CreateSDKLogger(&server.Context{Viper: v}, out)
	if err != nil {
		t.Fatal(err)
	}
	zl, ok := logger.Impl().(*zerolog.Logger)
	if !ok {
		t.Fatalf("sdk logger is %T", logger.Impl())
	}
	return zl, v
}

// logLines writes the same lines through l: callers inside the working
// directory (as test builds report them) and module paths (as -trimpath
// release builds report them).
func logLines(l zerolog.Logger) {
	l = l.With().Str("module", "x/thorchain").Logger()
	l.Info().Caller().Int64("height", 28124001).Msg("POL Reserve deployed")
	l.Error().Caller().Err(errors.New("not found")).Str("pool", "BTC.BTC").Msg("fail to get pool")
	l.Debug().Caller().Msg("debug line")
	l.Warn().Str("caller", "gitlab.com/thorchain/thornode/v3/x/thorchain/manager_volume_current.go:42").Msg("trimmed caller")
	l.Info().Msg("no caller")
}

func TestConsoleLogOutputMatchesSDK(t *testing.T) {
	saved := zerolog.TimestampFunc
	defer func() { zerolog.TimestampFunc = saved }()
	zerolog.TimestampFunc = func() time.Time { return time.Date(2026, 10, 23, 15, 4, 0, 0, time.Local) }

	for _, noColorEnv := range []string{"", "1"} {
		t.Setenv("NO_COLOR", noColorEnv)
		for _, settings := range []map[string]any{
			{},
			{flags.FlagLogLevel: "info"},
			{flags.FlagLogLevel: "debug", flags.FlagLogNoColor: true},
			{flags.FlagLogLevel: "error", flags.FlagLogFormat: "plain"},
		} {
			var want, got bytes.Buffer
			sdk, _ := sdkConsoleLogger(t, settings, &want)
			zl, v := sdkConsoleLogger(t, settings, io.Discard)
			for _, l := range []*zerolog.Logger{sdk, consoleLogOutput(zl, v, &got)} {
				logLines(*l)
			}
			if want.Len() == 0 {
				t.Fatalf("%v: nothing logged", settings)
			}
			if got.String() != want.String() {
				t.Errorf("NO_COLOR=%q %v:\nsdk:  %q\nthis: %q", noColorEnv, settings, want.String(), got.String())
			}
		}
	}
}

// With --trace the SDK logger adds error stacks; the stack's JSON key order
// varies from line to line, so this only checks the stack is kept.
func TestConsoleLogOutputKeepsTrace(t *testing.T) {
	var got bytes.Buffer
	zl, v := sdkConsoleLogger(t, map[string]any{flags.FlagLogLevel: "info", server.FlagTrace: true}, io.Discard)
	logLines(*consoleLogOutput(zl, v, &got))
	if !strings.Contains(got.String(), "stack=") {
		t.Errorf("no error stack: %q", got.String())
	}
}

func TestConsoleLogOutputKeepsOtherWriters(t *testing.T) {
	for _, settings := range []map[string]any{
		{flags.FlagLogFormat: flags.OutputFormatJSON},
		{flags.FlagLogLevel: "consensus:info,*:error"},
	} {
		zl, v := sdkConsoleLogger(t, settings, io.Discard)
		if consoleLogOutput(zl, v, io.Discard) != zl {
			t.Errorf("%v: logger was replaced", settings)
		}
	}
}

func BenchmarkConsoleLogCaller(b *testing.B) {
	sdk, v := sdkConsoleLogger(b, map[string]any{flags.FlagLogLevel: "info"}, io.Discard)
	for _, bc := range []struct {
		name   string
		logger *zerolog.Logger
	}{
		{"sdk", sdk},
		{"cached-cwd", consoleLogOutput(sdk, v, io.Discard)},
	} {
		l := bc.logger.With().Str("module", "x/thorchain").Logger()
		b.Run(bc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				l.Error().Str("caller", "gitlab.com/thorchain/thornode/v3/x/thorchain/manager_volume_current.go:42").
					Str("pool", "BTC.BTC").Msg("fail to get volume")
			}
		})
	}
}
