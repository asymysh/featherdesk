package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{DEBUG, "DEBUG"},
		{INFO, "INFO"},
		{WARN, "WARN"},
		{ERROR, "ERROR"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	tests := []struct {
		name     string
		minLevel Level
		logLevel Level
		wantLog  bool
	}{
		{"debug at debug level", DEBUG, DEBUG, true},
		{"info at debug level", DEBUG, INFO, true},
		{"warn at debug level", DEBUG, WARN, true},
		{"error at debug level", DEBUG, ERROR, true},
		{"debug at info level", INFO, DEBUG, false},
		{"info at info level", INFO, INFO, true},
		{"warn at info level", INFO, WARN, true},
		{"error at info level", INFO, ERROR, true},
		{"debug at warn level", WARN, DEBUG, false},
		{"info at warn level", WARN, INFO, false},
		{"warn at warn level", WARN, WARN, true},
		{"error at warn level", WARN, ERROR, true},
		{"debug at error level", ERROR, DEBUG, false},
		{"info at error level", ERROR, INFO, false},
		{"warn at error level", ERROR, WARN, false},
		{"error at error level", ERROR, ERROR, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := New(&buf, tt.minLevel)
			l.Log(tt.logLevel, "test", "hello")
			got := buf.String()
			if tt.wantLog && got == "" {
				t.Error("expected log output, got nothing")
			}
			if !tt.wantLog && got != "" {
				t.Errorf("expected no log output, got %q", got)
			}
		})
	}
}

func TestLogFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, DEBUG)
	l.Info("capture", "frame acquired")

	line := buf.String()
	// Format: 2026-06-04 12:30:45.123 INFO [capture] frame acquired
	parts := strings.SplitN(line, " ", 4)
	if len(parts) < 4 {
		t.Fatalf("expected at least 4 parts, got %d: %q", len(parts), line)
	}

	// Date part: YYYY-MM-DD
	if len(parts[0]) != 10 || parts[0][4] != '-' {
		t.Errorf("date format wrong: %q", parts[0])
	}

	// Time part: HH:MM:SS.mmm
	if !strings.Contains(parts[1], ":") || !strings.Contains(parts[1], ".") {
		t.Errorf("time format wrong: %q", parts[1])
	}

	// Level + module + message
	rest := parts[2] + " " + parts[3]
	if !strings.Contains(rest, "INFO") {
		t.Errorf("expected INFO level in output: %q", rest)
	}
	if !strings.Contains(rest, "[capture]") {
		t.Errorf("expected [capture] module in output: %q", rest)
	}
	if !strings.Contains(rest, "frame acquired") {
		t.Errorf("expected message in output: %q", rest)
	}
}

func TestLogMethods(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, DEBUG)

	l.Debug("mod", "debug msg")
	if !strings.Contains(buf.String(), "DEBUG") {
		t.Error("Debug() should contain DEBUG level")
	}
	buf.Reset()

	l.Info("mod", "info msg")
	if !strings.Contains(buf.String(), "INFO") {
		t.Error("Info() should contain INFO level")
	}
	buf.Reset()

	l.Warn("mod", "warn msg")
	if !strings.Contains(buf.String(), "WARN") {
		t.Error("Warn() should contain WARN level")
	}
	buf.Reset()

	l.Error("mod", "error msg")
	if !strings.Contains(buf.String(), "ERROR") {
		t.Error("Error() should contain ERROR level")
	}
}

func TestVerboseAndQuiet(t *testing.T) {
	var buf bytes.Buffer

	// Verbose mode = DEBUG level
	l := New(&buf, DEBUG)
	l.Debug("mod", "verbose message")
	if buf.Len() == 0 {
		t.Error("verbose mode should show debug messages")
	}
	buf.Reset()

	// Quiet mode = ERROR level
	l = New(&buf, ERROR)
	l.Info("mod", "info message")
	if buf.Len() != 0 {
		t.Error("quiet mode should suppress info messages")
	}
	l.Error("mod", "error message")
	if buf.Len() == 0 {
		t.Error("quiet mode should still show errors")
	}
}
