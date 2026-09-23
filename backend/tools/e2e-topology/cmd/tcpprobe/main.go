// Command mm44-tcpprobe performs bounded TCP connectivity checks inside OrbStack VM guests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	probeBinaryName = "mm44-tcpprobe"
	minProbePort    = 1024
	maxProbePort    = 65535
)

var probeRuntimeDir = "/run/mm44-topology"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mm44-tcpprobe {connect|serve|stop}")
	}

	switch args[0] {
	case "connect":
		return runConnect(args[1:])
	case "serve":
		return runServe(args[1:])
	case "stop":
		return runStop(args[1:])
	default:
		return errors.New("usage: mm44-tcpprobe {connect|serve|stop}")
	}
}

func runConnect(args []string) error {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	address := flags.String("address", "", "target IPv4 address and port")
	timeout := flags.Duration("timeout", 3*time.Second, "connection timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("tcpprobe: unexpected connect arguments")
	}
	if err := validateAddress(*address); err != nil {
		return err
	}
	if *timeout <= 0 || *timeout > 10*time.Second {
		return errors.New("tcpprobe: timeout must be between 0 and 10s")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp4", *address)
	if err != nil {
		return errors.New("tcpprobe: connection failed")
	}
	if err := connection.Close(); err != nil {
		return errors.New("tcpprobe: closing connection failed")
	}
	return nil
}

func runServe(args []string) (returnErr error) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := flags.Int("port", 0, "listen port")
	lifetime := flags.Duration("lifetime", 20*time.Second, "maximum server lifetime")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("tcpprobe: unexpected serve arguments")
	}
	if err := validatePort(*port); err != nil {
		return err
	}
	if *lifetime <= 0 || *lifetime > time.Minute {
		return errors.New("tcpprobe: lifetime must be between 0 and 1m")
	}

	if err := os.MkdirAll(probeRuntimeDir, 0o750); err != nil {
		return errors.New("tcpprobe: creating runtime directory failed")
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, *lifetime)
	defer cancel()

	listener, err := (&net.ListenConfig{}).Listen(
		ctx,
		"tcp4",
		net.JoinHostPort("0.0.0.0", strconv.Itoa(*port)),
	)
	if err != nil {
		return errors.New("tcpprobe: listening failed")
	}
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		return errors.New("tcpprobe: listener is not TCP")
	}
	defer func() {
		returnErr = errors.Join(returnErr, listener.Close())
	}()

	pidPath := probePath(*port, "pid")
	readyPath := probePath(*port, "ready")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return errors.New("tcpprobe: writing pid file failed")
	}
	defer func() {
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, errors.New("tcpprobe: removing pid file failed"))
		}
	}()
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
		return errors.New("tcpprobe: writing ready file failed")
	}
	defer func() {
		if err := os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, errors.New("tcpprobe: removing ready file failed"))
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := tcpListener.SetDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			return errors.New("tcpprobe: setting listener deadline failed")
		}
		connection, err := listener.Accept()
		if err != nil {
			var networkErr net.Error
			if errors.As(err, &networkErr) && networkErr.Timeout() {
				continue
			}
			return errors.New("tcpprobe: accepting connection failed")
		}
		if err := connection.Close(); err != nil {
			return errors.New("tcpprobe: closing accepted connection failed")
		}
	}
}

func runStop(args []string) error {
	flags := flag.NewFlagSet("stop", flag.ContinueOnError)
	port := flags.Int("port", 0, "listen port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("tcpprobe: unexpected stop arguments")
	}
	if err := validatePort(*port); err != nil {
		return err
	}

	pidBytes, err := os.ReadFile(probePath(*port, "pid"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("tcpprobe: reading pid file failed")
	}
	pid, err := strconv.Atoi(stringTrimSpace(pidBytes))
	if err != nil || pid <= 1 {
		return errors.New("tcpprobe: invalid pid file")
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || filepath.Base(executable) != probeBinaryName {
		return errors.New("tcpprobe: refusing to stop an unexpected process")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.New("tcpprobe: stopping server failed")
	}
	return nil
}

func validateAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("tcpprobe: address must be IPv4:port")
	}
	if parsed := net.ParseIP(host); parsed == nil || parsed.To4() == nil {
		return errors.New("tcpprobe: address host must be IPv4")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("tcpprobe: address port is invalid")
	}
	return validatePort(port)
}

func validatePort(port int) error {
	if port < minProbePort || port > maxProbePort {
		return errors.New("tcpprobe: port must be between 1024 and 65535")
	}
	return nil
}

func probePath(port int, extension string) string {
	return filepath.Join(probeRuntimeDir, fmt.Sprintf("probe-%d.%s", port, extension))
}

func stringTrimSpace(value []byte) string {
	start := 0
	for start < len(value) && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	end := len(value)
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return string(value[start:end])
}
