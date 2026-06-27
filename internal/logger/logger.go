// Package logger provides NestJS-style structured console logging.
//
// Output format:
//
//	[VaultNUBAN] 12345  - 01/27/2026, 3:04:05 PM     LOG [Bootstrap] message
//	[VaultNUBAN] 12345  - 01/27/2026, 3:04:05 PM   ERROR [WebhookHandler] something failed
package logger

import (
	"fmt"
	"io"
	"os"
	"time"
)

// ANSI colour codes
const (
	reset   = "\033[0m"
	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	gray    = "\033[90m"
)

var (
	pid     = os.Getpid()
	appName = "VaultNUBAN"
	out     io.Writer = os.Stdout
	errOut  io.Writer = os.Stderr
	useColor           = isTerminal(os.Stdout)
)

// ── Public API ────────────────────────────────────────────────────────────────

// Log writes a LOG-level message. Use for normal lifecycle events.
func Log(context, message string) { write(out, "LOG", green, context, message) }

// Error writes an ERROR-level message to stderr.
func Error(context, message string) { write(errOut, "ERROR", red, context, message) }

// Errorf is a formatted variant of Error.
func Errorf(context, format string, args ...any) {
	Error(context, fmt.Sprintf(format, args...))
}

// Warn writes a WARN-level message.
func Warn(context, message string) { write(out, "WARN", yellow, context, message) }

// Warnf is a formatted variant of Warn.
func Warnf(context, format string, args ...any) {
	Warn(context, fmt.Sprintf(format, args...))
}

// Debug writes a DEBUG-level message.
func Debug(context, message string) { write(out, "DEBUG", magenta, context, message) }

// Debugf is a formatted variant of Debug.
func Debugf(context, format string, args ...any) {
	Debug(context, fmt.Sprintf(format, args...))
}

// Verbose writes a VERBOSE-level message.
func Verbose(context, message string) { write(out, "VERBOSE", cyan, context, message) }

// Logf is a formatted variant of Log.
func Logf(context, format string, args ...any) {
	Log(context, fmt.Sprintf(format, args...))
}

// ── Internal ──────────────────────────────────────────────────────────────────

// levelWidth is the column width for the level label (matches NestJS alignment).
const levelWidth = 7

func write(w io.Writer, level, colour, context, message string) {
	ts := time.Now().Format("01/02/2006, 3:04:05 PM")

	if !useColor {
		fmt.Fprintf(w, "[%s] %d  - %s     %*s [%s] %s\n",
			appName, pid, ts, levelWidth, level, context, message)
		return
	}

	// [VaultNUBAN]  pid  -  timestamp      LEVEL  [Context]  message
	fmt.Fprintf(w, "%s[%s]%s %s%d%s  - %s%s%s  %s%*s%s %s[%s]%s %s%s%s\n",
		yellow, appName, reset,
		green, pid, reset,
		gray, ts, reset,
		colour, levelWidth, level, reset,
		yellow, context, reset,
		colour, message, reset,
	)
}

// isTerminal returns true when fd is connected to a real TTY.
// Falls back to false on any error so non-TTY environments get plain text.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	// ModeCharDevice is set for TTYs on both Unix and Windows.
	return fi.Mode()&os.ModeCharDevice != 0
}

// SetOutput overrides the writer used for non-error output (useful in tests).
func SetOutput(w io.Writer) { out = w; useColor = false }
