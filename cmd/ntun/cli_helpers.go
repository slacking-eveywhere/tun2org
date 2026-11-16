package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"ntun-ng/internal/config"
)

// configInit writes a commented default config to path, creating parents.
func configInit(path string) int {
	if err := os.MkdirAll(parentOf(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "ntun: %s already exists\n", path)
		return 2
	}
	example := config.Config{
		Username:        "your-login",
		ForwardServer:   "bla.bli.blou",
		Auth:            config.AuthPassword,
		Routes:          []string{"10.120.1.10/32", "10.135.0.0/16"},
		TunDevice:       "tun0",
		TunNetwork:      "198.18.0.1/15",
		PingEvery:       30 * time.Second,
		ReconnectMin:    1 * time.Second,
		ReconnectMax:    30 * time.Second,
		PostStartScript: "",
		PostStopScript:  "",
	}
	data, err := json.MarshalIndent(example, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	fmt.Fprintf(os.Stderr, "ntun: wrote %s\n", path)
	return 0
}

// configEdit opens the config in $EDITOR (or vi).
func configEdit(path string) int {
	if err := os.MkdirAll(parentOf(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ntun: %v\n", err)
		return 2
	}
	if _, err := os.Stat(path); err != nil {
		if c := configInit(path); c != 0 {
			return c
		}
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "ntun: editor: %v\n", err)
		return 2
	}
	return 0
}

func parentOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}
