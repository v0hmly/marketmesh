package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/frontdoor"
)

const (
	defaultListenAddress = "127.0.0.1:18080"
	defaultShutdown      = 10 * time.Second
	maxRequestBytes      = int64(64 * 1024)
)

type options struct {
	listenAddress  string
	dcATarget      string
	dcBTarget      string
	healthInterval time.Duration
	healthTimeout  time.Duration
	failbackWarmup time.Duration
	shutdown       time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	configuration, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	if err := validateListenAddress(configuration.listenAddress); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	frontDoor, err := frontdoor.New(frontdoor.Config{
		DCATarget:           configuration.dcATarget,
		DCBTarget:           configuration.dcBTarget,
		HealthCheckInterval: configuration.healthInterval,
		HealthCheckTimeout:  configuration.healthTimeout,
		FailbackWarmup:      configuration.failbackWarmup,
		Logger:              logger,
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", configuration.listenAddress)
	if err != nil {
		return fmt.Errorf("frontdoor: listening: %w", err)
	}

	server := &http.Server{
		Handler:           http.MaxBytesHandler(frontDoor.Handler(), maxRequestBytes),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 * 1024,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	errorChannel := make(chan error, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Go(func() {
		if runErr := frontDoor.Run(runContext); runErr != nil &&
			!errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			errorChannel <- fmt.Errorf("frontdoor: checking health: %w", runErr)
		}
	})
	waitGroup.Go(func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorChannel <- fmt.Errorf("frontdoor: serving HTTP: %w", serveErr)
		}
	})
	logger.Info("локальный front door запущен", slog.String("listen_address", listener.Addr().String()))

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errorChannel:
	}
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), configuration.shutdown)
	shutdownErr := server.Shutdown(shutdownContext)
	shutdownCancel()
	waitGroup.Wait()
	logger.Info("локальный front door остановлен")

	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("frontdoor: shutting down HTTP: %w", shutdownErr)
	}
	return errors.Join(runErr, shutdownErr)
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("tunnel-frontdoor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var result options
	flags.StringVar(&result.listenAddress, "listen", defaultListenAddress, "loopback listen address")
	flags.StringVar(&result.dcATarget, "dc-a-target", "", "private literal HTTP target for dc-a")
	flags.StringVar(&result.dcBTarget, "dc-b-target", "", "private literal HTTP target for dc-b")
	flags.DurationVar(&result.healthInterval, "health-interval", time.Second, "backend health interval")
	flags.DurationVar(&result.healthTimeout, "health-timeout", 250*time.Millisecond, "backend health timeout")
	flags.DurationVar(&result.failbackWarmup, "failback-warmup", 30*time.Second, "recovered DC warmup")
	flags.DurationVar(&result.shutdown, "shutdown-timeout", defaultShutdown, "graceful shutdown timeout")
	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("frontdoor: parsing flags: %w", err)
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("frontdoor: positional arguments are not supported")
	}
	if result.dcATarget == "" || result.dcBTarget == "" {
		return options{}, errors.New("frontdoor: both data-center targets are required")
	}
	if result.shutdown <= 0 || result.shutdown > time.Minute {
		return options{}, errors.New("frontdoor: shutdown timeout is outside bounds")
	}
	return result, nil
}

func validateListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("frontdoor: listen address must contain a literal loopback IP and port")
	}
	parsedAddress, err := netip.ParseAddr(host)
	if err != nil || !parsedAddress.Unmap().IsLoopback() {
		return errors.New("frontdoor: listen address must use a literal loopback IP")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return errors.New("frontdoor: listen port is outside bounds")
	}
	return nil
}
