package main

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/term"
)

// readPasswordFromTerminal reads a line from /dev/tty with echo disabled.
func readPasswordFromTerminal() (string, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// Fall back to stdin if no controlling terminal.
		f = os.Stdin
	} else {
		defer f.Close()
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		// Not a terminal: read a line plainly (e.g. piped input).
		var line []byte
		buf := make([]byte, 1)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if buf[0] == '\n' {
					break
				}
				line = append(line, buf[0])
			}
			if err != nil {
				break
			}
		}
		return string(line), nil
	}
	b, err := term.ReadPassword(fd)
	if err != nil {
		// EINTR is benign on signal.
		if err2, ok := err.(syscall.Errno); ok && err2 == syscall.EINTR {
			return string(b), nil
		}
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}
