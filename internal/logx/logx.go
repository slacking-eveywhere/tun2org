// Package logx is the minimal printf-style logging surface shared by the
// ntun packages: a plain func type instead of per-package Logger interfaces.
package logx

import (
	"fmt"
	"io"
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
