package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aseem/viewport-rds/internal/capture"
	"github.com/aseem/viewport-rds/internal/encode"
	"github.com/aseem/viewport-rds/internal/logger"
	"github.com/aseem/viewport-rds/internal/server"
)

//go:embed all:client
var clientFS embed.FS

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

func setupSignalHandler() (context.Context, context.CancelFunc, chan os.Signal) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		cancel()
		<-sigCh
		os.Exit(1)
	}()

	return ctx, cancel, sigCh
}

func main() {
	cfg := parseFlags()
	log := newLogger(cfg)
	log.Info("main", fmt.Sprintf("viewport-rds starting on port %d at %d fps", cfg.port, cfg.fps))

	ctx, cancel, _ := setupSignalHandler()
	defer cancel()

	capturer, err := capture.NewKMSCapturer(ctx, cfg.fps)
	if err != nil {
		log.Error("main", "capture: "+err.Error())
		os.Exit(1)
	}
	defer capturer.Close()
	log.Info("main", "capture: KMS capturer initialized")

	clientContent, err := fs.Sub(clientFS, "client")
	if err != nil {
		log.Error("main", "embed: "+err.Error())
		os.Exit(1)
	}

	srv := server.New(server.Config{
		Port:     cfg.port,
		Log:      log,
		ClientFS: clientContent,
	})

	var converter *encode.Converter
	var enc *encode.H264Encoder

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		frameDuration := time.Second / time.Duration(cfg.fps)
		for {
			if ctx.Err() != nil {
				return
			}

			start := time.Now()
			frame, err := capturer.NextFrame()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error("capture", err.Error())
				time.Sleep(100 * time.Millisecond)
				continue
			}

			w, h := int(frame.Width), int(frame.Height)

			if converter == nil {
				converter = encode.NewConverter(w, h)
				enc, err = encode.NewH264Encoder(encode.EncoderConfig{
					Width:  w,
					Height: h,
					FPS:    cfg.fps,
					QP:     26,
				})
				if err != nil {
					log.Error("main", "encoder init: "+err.Error())
					cancel()
					return
				}
				defer enc.Close()
				srv.SetNewClientCallback(func() {
					enc.ForceKeyframe()
				})
				log.Info("main", fmt.Sprintf("encode: %dx%d H.264 QP=26", w, h))
			}

			i420 := converter.Convert(frame.Data)
			nals, err := enc.Encode(i420)
			if err != nil {
				log.Error("encode", err.Error())
				continue
			}

			if len(nals) > 0 {
				srv.Broadcast(nals, uint16(w), uint16(h), frame.Timestamp)
			}

			elapsed := time.Since(start)
			if elapsed < frameDuration {
				time.Sleep(frameDuration - elapsed)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Start(ctx); err != nil {
			log.Error("server", err.Error())
			cancel()
		}
	}()

	<-ctx.Done()
	log.Info("main", "shutting down")
	wg.Wait()
	log.Info("main", "stopped")
}
