package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docker-watchdog/internal/display"
	"docker-watchdog/internal/dockerengine"
	"docker-watchdog/internal/httpapi"
	"docker-watchdog/internal/storage"
	"docker-watchdog/internal/tui"
	"docker-watchdog/internal/watchdog"
	"github.com/moby/moby/client"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "watchdog:", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	if config.version {
		fmt.Println(version)
		return nil
	}
	interactive := term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
	if config.output == "auto" {
		config.output = "logs"
		if interactive {
			config.output = "table"
		}
	}
	if config.output == "table" && !interactive {
		return errors.New("table output requires an interactive terminal; use --output logs or json")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if config.runFor > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.runFor)
		defer cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	openCtx, openCancel := context.WithTimeout(ctx, config.monitor.APITimeout)
	journal, err := storage.Open(openCtx, config.database)
	openCancel()
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer journal.Close()

	clientOptions := []client.Opt{client.FromEnv}
	if config.host != "" {
		clientOptions = append(clientOptions, client.WithHost(config.host))
	}
	docker, err := client.New(clientOptions...)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer docker.Close()

	serverErrors := make(chan error, 1)
	if config.listen != "" {
		listener, err := net.Listen("tcp", config.listen)
		if err != nil {
			return fmt.Errorf("listen for API: %w", err)
		}
		server := httpapi.New(config.listen, journal)
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErrors <- err
				cancel()
			}
		}()
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutdownCancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				server.Close()
			}
		}()
		fmt.Fprintln(os.Stderr, "read-only API: http://"+listener.Addr().String())
	}

	fmt.Fprintf(os.Stderr, "watchdog %s: auto-restart=%t label=%q endpoint=%s\n",
		version, config.monitor.AutoRestart, config.label, docker.DaemonHost())

	events := make(chan watchdog.Event, 128)
	var controls *watchdog.Controller
	if config.output == "table" {
		controls = watchdog.NewController()
	}
	done := make(chan error, 1)
	engine := &dockerengine.Engine{Client: docker, Label: config.label}
	runner := watchdog.Runner{
		Engine:   engine,
		Config:   config.monitor,
		Journal:  journal,
		Controls: controls,
	}
	go func() {
		done <- runner.Run(ctx, events)
	}()

	var outputError error
	var processStopped bool
	var cleanUIExit bool
	if config.output == "table" {
		outputError = tui.Run(ctx, events, tui.Options{
			AutoRestart: config.monitor.AutoRestart,
			Label:       config.label,
			Endpoint:    docker.DaemonHost(),
			MaxRetries:  config.monitor.MaxRetries,
			Version:     version,
			Control:     controls.Do,
			Logs:        engine.Logs,
		}, os.Stdin, os.Stdout)
		cleanUIExit = outputError == nil
		processStopped = ctx.Err() != nil
		if processStopped {
			outputError = nil
		}
		cancel()
	} else {
		output := display.New(config.output, os.Stdout)
		for event := range events {
			if err := output.Accept(event); err != nil {
				outputError = err
				cancel()
				break
			}
		}
		processStopped = ctx.Err() != nil
	}
	// Drain after early UI exit or a broken pipe so no worker can be left
	// waiting on the bounded fan-in channel.
	for range events {
	}
	runError := <-done

	select {
	case err := <-serverErrors:
		return fmt.Errorf("serve API: %w", err)
	default:
	}
	if outputError != nil {
		return outputError
	}
	if cleanUIExit && errors.Is(runError, context.Canceled) {
		return nil
	}
	if processStopped && (errors.Is(runError, context.Canceled) || errors.Is(runError, context.DeadlineExceeded)) {
		return nil
	}
	return runError
}
