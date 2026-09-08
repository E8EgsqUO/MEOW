package main

import "testing"

func TestColoredLogPrefix(t *testing.T) {
	original := colorize
	t.Cleanup(func() { colorize = original })

	colorize = false
	if got := coloredLogPrefix("31", "[ERROR] "); got != "[ERROR] " {
		t.Fatalf("plain prefix = %q", got)
	}
	colorize = true
	if got := coloredLogPrefix("31", "[ERROR] "); got != "\x1b[31m[ERROR] \x1b[0m" {
		t.Fatalf("colored prefix = %q", got)
	}
}

func TestConfiguredLogFile(t *testing.T) {
	originalConfig := config
	originalDebug := debug
	t.Cleanup(func() {
		config = originalConfig
		debug = originalDebug
	})

	tests := []struct {
		name     string
		debug    bool
		logFile  string
		expected string
	}{
		{name: "normal logging stays on stdout"},
		{name: "debug defaults to current directory", debug: true, expected: defaultDebugLogFile},
		{name: "configured path takes precedence", debug: true, logFile: "custom.log", expected: "custom.log"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			debug = debugLogging(tt.debug)
			config.LogFile = tt.logFile
			if got := configuredLogFile(); got != tt.expected {
				t.Fatalf("configuredLogFile() = %q, want %q", got, tt.expected)
			}
		})
	}
}
