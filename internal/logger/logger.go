package logger

import (
	"fmt"
	"io"
	"time"
)

// Level represents log severity.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger is a leveled, timestamped logger.
type Logger struct {
	out   io.Writer
	level Level
}

// New creates a logger writing to out, filtering messages below minLevel.
func New(out io.Writer, minLevel Level) *Logger {
	return &Logger{out: out, level: minLevel}
}

// Log writes a message if the given level meets the minimum threshold.
func (l *Logger) Log(level Level, module, msg string) {
	if level < l.level {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(l.out, "%s %s [%s] %s\n", ts, level, module, msg)
}

func (l *Logger) Debug(module, msg string) { l.Log(DEBUG, module, msg) }
func (l *Logger) Info(module, msg string)  { l.Log(INFO, module, msg) }
func (l *Logger) Warn(module, msg string)  { l.Log(WARN, module, msg) }
func (l *Logger) Error(module, msg string) { l.Log(ERROR, module, msg) }
