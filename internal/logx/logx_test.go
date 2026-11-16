package logx_test

import (
	"bytes"
	"os"
	"testing"

	"ntun-ng/internal/logx"
)

func TestTag(t *testing.T) {
	cases := map[string]string{
		"internal_server": "[int]",
		"external_server": "[ext]",
		"ab":              "[ab]",
		"PRODUCTION":      "[pro]",
		"ünicode":         "[üni]",
	}
	for name, want := range cases {
		if got := logx.Tag(name); got != want {
			t.Errorf("Tag(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestColorize(t *testing.T) {
	var buf bytes.Buffer
	log := logx.Colorize(logx.ToWriter(&buf), "[int]", logx.FgCyan)
	log("hello %s", "world")
	want := "\x1b[36m[int]\x1b[0m: hello world\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestColorizeNil(t *testing.T) {
	log := logx.Colorize(nil, "[int]", logx.FgRed)
	log("boom") // must not panic
}

func TestColorizeUnicode(t *testing.T) {
	var buf bytes.Buffer
	log := logx.Colorize(logx.ToWriter(&buf), "[ünü]", logx.FgMagenta)
	log("x")
	want := "\x1b[35m[ünü]\x1b[0m: x\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestPrefixPlainMatchesColorlessTag(t *testing.T) {
	var buf bytes.Buffer
	logx.Prefix(logx.ToWriter(&buf), logx.Tag("internal_server"))("hi")
	if buf.String() != "[int]: hi\n" {
		t.Errorf("plain prefix = %q", buf.String())
	}
}

func TestColorEnabledNonFileWriterFalse(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("TERM", "xterm")
	if logx.ColorEnabled(&buf) {
		t.Error("ColorEnabled(buffer) should be false")
	}
}

func TestColorEnv(t *testing.T) {
	// /dev/null is a character device, so the env half is the only gate.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skip("no /dev/null")
	}
	defer devNull.Close()

	// TERM set and normal -> on (char device + valid TERM).
	t.Setenv("TERM", "xterm")
	if !logx.ColorEnabled(devNull) {
		t.Error("TERM=xterm on char device should enable color")
	}
	// NO_COLOR set -> off.
	t.Setenv("NO_COLOR", "1")
	if logx.ColorEnabled(devNull) {
		t.Error("NO_COLOR ignored")
	}
	// TERM=dumb -> off.
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "dumb")
	if logx.ColorEnabled(devNull) {
		t.Error("TERM=dumb ignored")
	}
	// TERM unset -> off.
	t.Setenv("TERM", "")
	if logx.ColorEnabled(devNull) {
		t.Error("empty TERM ignored")
	}
}

func TestPaletteRotation(t *testing.T) {
	if len(logx.Palette) < 6 {
		t.Fatalf("palette too small: %v", logx.Palette)
	}
	seen := map[int]bool{}
	for _, c := range logx.Palette {
		if seen[c] {
			t.Fatalf("duplicate color %d in palette", c)
		}
		seen[c] = true
	}
}

func TestPrefixNilLogger(t *testing.T) {
	logx.Prefix(nil, "x")("y") // must not panic
}
