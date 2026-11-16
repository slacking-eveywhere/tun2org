// Package logx is the minimal printf-style logging surface shared by the
// ntun packages: a plain func type instead of per-package Logger interfaces.
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Logger is a printf-style, line-oriented logging function.
type Logger func(format string, args ...any)

// Discard drops everything. It is the zero-logger default.
func Discard(string, ...any) {}

// ToWriter returns a Logger that writes one line per call to w (e.g.
// os.Stderr).
func ToWriter(w io.Writer) Logger {
	return func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
	}
}

// Prefix returns a Logger that prefixes every line of l with name. A nil l
// becomes Discard.
func Prefix(l Logger, name string) Logger {
	if l == nil {
		l = Discard
	}
	return func(format string, args ...any) {
		l(name+": "+format, args...)
	}
}

// ANSI foreground color codes (docker compose palette).
const (
	FgCyan    = 36
	FgYellow  = 33
	FgGreen   = 32
	FgMagenta = 35
	FgRed     = 31
	FgBlue    = 34
)

// Palette is the per-profile color rotation, in file order (docker compose
// style: cyan, yellow, green, magenta, red, blue, then cycles).
var Palette = []int{FgCyan, FgYellow, FgGreen, FgMagenta, FgRed, FgBlue}

// Tag returns the log tag of a profile name: the first three characters in
// brackets, lowercased ("internal_server" -> "[int]").
func Tag(name string) string {
	r := []rune(strings.ToLower(name))
	if len(r) > 3 {
		r = r[:3]
	}
	return "[" + string(r) + "]"
}

// Colorize returns a Logger whose tag is printed in the given ANSI color:
//
//	\x1b[<code>m[int]\x1b[0m: <message>
//
// The message itself stays uncolored. A nil l becomes Discard.
func Colorize(l Logger, tag string, code int) Logger {
	if l == nil {
		l = Discard
	}
	return func(format string, args ...any) {
		l("\x1b[%dm%s\x1b[0m: "+format, append([]any{code, tag}, args...)...)
	}
}

// ColorEnabled reports whether colors should be used on w: w must be a
// character device, TERM must be set and not "dumb", and NO_COLOR must be
// unset (https://no-color.org).
func ColorEnabled(w io.Writer) bool {
	return charDevice(w) && colorEnvOK()
}

func charDevice(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func colorEnvOK() bool {
	if _, no := os.LookupEnv("NO_COLOR"); no {
		return false
	}
	term := os.Getenv("TERM")
	return term != "" && term != "dumb"
}
