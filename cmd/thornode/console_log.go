package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"github.com/cosmos/cosmos-sdk/client/flags"
)

// consoleLogOutput returns zl, the SDK's server logger, writing through a
// console writer that looks up the working directory once. zerolog's default
// caller formatter calls os.Getwd (which stats the directory) for every line
// to shorten the caller path, and the block thread logs a lot.
//
// The writer is otherwise the one cosmossdk.io/log builds for the server's log
// flags, and zerolog's Output keeps the logger's level, hooks and context. For
// JSON output and for module filtered log levels, which the SDK writes through
// writers this can't rebuild, zl is returned as is.
func consoleLogOutput(zl *zerolog.Logger, v *viper.Viper, out io.Writer) *zerolog.Logger {
	if v.GetString(flags.FlagLogFormat) == flags.OutputFormatJSON {
		return zl
	}
	if lvl := v.GetString(flags.FlagLogLevel); lvl != "" {
		if _, err := zerolog.ParseLevel(lvl); err != nil {
			return zl
		}
	}
	noColor := v.GetBool(flags.FlagLogNoColor)
	logger := zl.Output(zerolog.ConsoleWriter{
		Out:          out,
		NoColor:      noColor,
		TimeFormat:   time.Kitchen,
		FormatCaller: consoleFormatCaller(noColor),
	})
	return &logger
}

// consoleFormatCaller is zerolog's default caller formatter with the working
// directory looked up once.
func consoleFormatCaller(noColor bool) zerolog.Formatter {
	cwd, cwdErr := os.Getwd()
	return func(i any) string {
		var c string
		if cc, ok := i.(string); ok {
			c = cc
		}
		if len(c) > 0 {
			if cwdErr == nil {
				if rel, err := filepath.Rel(cwd, c); err == nil {
					c = rel
				}
			}
			c = consoleColorize(c, 1, noColor) + consoleColorize(" >", 36, noColor)
		}
		return c
	}
}

// consoleColorize is zerolog's colorize, which isn't exported.
func consoleColorize(s any, c int, disabled bool) string {
	e := os.Getenv("NO_COLOR")
	if e != "" || c == 0 {
		disabled = true
	}

	if disabled {
		return fmt.Sprintf("%s", s)
	}
	return fmt.Sprintf("\x1b[%dm%v\x1b[0m", c, s)
}
