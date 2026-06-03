package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aseem/viewport-rds/internal/audio"
	"github.com/aseem/viewport-rds/internal/capture"
	"github.com/aseem/viewport-rds/internal/encode"
	"github.com/aseem/viewport-rds/internal/input"
	"github.com/aseem/viewport-rds/internal/logger"
	"github.com/aseem/viewport-rds/internal/server"
)

//go:embed all:client
var clientFS embed.FS

type config struct {
	port     int
	fps      int
	verbose  bool
	quiet    bool
	logFile  string
	hardware bool
	software bool
	noAudio  bool
	bind     string
}

func parseFlags() config {
	cfg := config{}
	flag.IntVar(&cfg.port, "port", 30084, "HTTP server port")
	flag.IntVar(&cfg.fps, "fps", 30, "Target frames per second")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Enable debug logging")
	flag.BoolVar(&cfg.quiet, "quiet", false, "Suppress all output except errors")
	flag.StringVar(&cfg.logFile, "log-file", "", "Log to file instead of stderr")
	flag.BoolVar(&cfg.hardware, "hardware", false, "Force VA-API hardware encoding")
	flag.BoolVar(&cfg.software, "software", false, "Force OpenH264 software encoding")
	flag.BoolVar(&cfg.noAudio, "no-audio", false, "Disable audio capture")
	flag.StringVar(&cfg.bind, "bind", "0.0.0.0", "Bind address")
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

func checkUInput() bool {
	_, err := os.Stat("/dev/uinput")
	return err == nil
}

func checkPipeWire() bool {
	_, err := exec.LookPath("pw-cat")
	return err == nil
}

func checkFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func main() {
	cfg := parseFlags()
	log := newLogger(cfg)

	log.Info("main", "ViewPort RDS v0.1.0")
	log.Info("main", fmt.Sprintf("listening on http://%s:%d/", cfg.bind, cfg.port))

	capKMS := os.Geteuid() == 0
	capVAAPI := encode.ProbeVAAPI()
	capUInput := checkUInput()
	capPipeWire := checkPipeWire()
	capFFmpeg := checkFFmpeg()

	log.Info("main", fmt.Sprintf("capabilities: KMS=%v VA-API=%v uinput=%v PipeWire=%v ffmpeg=%v",
		capKMS, capVAAPI, capUInput, capPipeWire, capFFmpeg))

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
		Bind:     cfg.bind,
		Log:      log,
		ClientFS: clientContent,
	})

	// Input injection (best-effort: non-fatal if uinput unavailable)
	inputDev, inputErr := input.NewDevice(2560, 1440)
	if inputErr != nil {
		log.Info("main", "input: "+inputErr.Error()+" (input disabled)")
	} else {
		defer inputDev.Close()
		srv.SetInputCallback(func(data []byte) {
			msg, err := input.ParseMessage(data)
			if err != nil {
				return
			}
			inputDev.HandleMessage(msg)
		})
		log.Info("main", "input: uinput device created")
	}

	var converter *encode.Converter
	var enc encode.Encoder

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
				encCfg := encode.EncoderConfig{
					Width:  w,
					Height: h,
					FPS:    cfg.fps,
					QP:     26,
				}

				var encoder encode.Encoder
				useHW := cfg.hardware || (!cfg.software && encode.ProbeVAAPI())

				if cfg.hardware && !encode.ProbeVAAPI() {
					log.Error("main", "VA-API hardware encoding requested but not available")
					cancel()
					return
				}

				if useHW {
					encoder, err = encode.NewFFmpegEncoder(encCfg, log, true)
					if err != nil {
						log.Error("main", "ffmpeg hw encoder: "+err.Error())
						cancel()
						return
					}
					log.Info("main", fmt.Sprintf("encode: %dx%d H.264 VA-API (hardware)", w, h))
				} else {
					encoder, err = encode.NewH264Encoder(encCfg)
					if err != nil {
						log.Error("main", "encoder init: "+err.Error())
						cancel()
						return
					}
					log.Info("main", fmt.Sprintf("encode: %dx%d H.264 OpenH264 (software)", w, h))
				}

				enc = encoder
				defer enc.Close()
				if useHW {
					srv.SetEncoderType("h264_vaapi")
				} else {
					srv.SetEncoderType("openh264")
				}
				srv.SetNewClientCallback(func() {
					enc.ForceKeyframe()
				})
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

	if !cfg.noAudio {
		audioCap, audioErr := audio.NewCapturer(ctx, log)
		if audioErr != nil {
			log.Info("main", "audio: "+audioErr.Error()+" (audio disabled)")
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer audioCap.Close()
				for chunk := range audioCap.Chunks() {
					srv.BroadcastAudio(chunk, uint64(time.Now().UnixMilli()))
				}
			}()
			log.Info("main", "audio: PipeWire capture started")
			srv.SetAudioEnabled(true)
		}
	} else {
		log.Info("main", "audio: disabled")
	}

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
