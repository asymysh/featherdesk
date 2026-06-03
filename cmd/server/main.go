package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/aseem/viewport-rds/internal/logger"
)

type config struct {
	port    int
	fps     int
	verbose bool
	quiet   bool
	logFile string
}

func parseFlags() config {
	cfg := config{}
	flag.IntVar(&cfg.port, "port", 30084, "HTTP server port")
	flag.IntVar(&cfg.fps, "fps", 30, "Target frames per second")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Enable debug logging")
	flag.BoolVar(&cfg.quiet, "quiet", false, "Suppress all output except errors")
	flag.StringVar(&cfg.logFile, "log-file", "", "Log to file instead of stderr")
	flag.Parse()
	return cfg
}

func newLogger(cfg config) *logger.Logger {
	var out *os.File
	if cfg.logFile != "" {
		f, err := os.OpenFile(cfg.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", err)
			os.Exit(1)
		}
		out = f
	} else {
		out = os.Stderr
	}

	level := logger.INFO
	if cfg.verbose {
		level = logger.DEBUG
	}
	if cfg.quiet {
		level = logger.ERROR
	}

	return logger.New(out, level)
}

func main() {
	cfg := parseFlags()
	log := newLogger(cfg)
	log.Info("main", fmt.Sprintf("viewport-rds starting on port %d at %d fps", cfg.port, cfg.fps))
}
