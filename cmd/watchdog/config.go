package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"docker-watchdog/internal/watchdog"
)

type options struct {
	monitor  watchdog.Config
	output   string
	label    string
	host     string
	database string
	listen   string
	runFor   time.Duration
	version  bool
}

func parseOptions(args []string, help io.Writer) (options, error) {
	config := options{monitor: watchdog.DefaultConfig()}
	flags := flag.NewFlagSet("watchdog", flag.ContinueOnError)
	flags.SetOutput(help)
	flags.Usage = func() {
		fmt.Fprintln(help, "Watchdog — concurrent Docker monitoring and bounded recovery")
		fmt.Fprintln(help, "\nUsage: watchdog [options]\n\nExamples:")
		fmt.Fprintln(help, "  watchdog                              # live dashboard in a terminal")
		fmt.Fprintln(help, "  watchdog --label watchdog.demo=true --auto-restart")
		fmt.Fprintln(help, "  watchdog --output json --listen 127.0.0.1:9780")
		fmt.Fprintln(help, "\nOptions:")
		flags.PrintDefaults()
	}
	flags.StringVar(&config.output, "output", "auto", "auto, table (interactive), logs, or json")
	flags.StringVar(&config.label, "label", "", "Docker label filter (key or key=value)")
	flags.StringVar(&config.host, "host", "", "Docker endpoint; defaults to DOCKER_HOST / SDK platform default")
	flags.StringVar(&config.database, "db", "watchdog.db", "SQLite file containing retry budgets and incident history")
	flags.StringVar(&config.listen, "listen", "127.0.0.1:9780", "read-only HTTP API address; empty string disables it")
	flags.DurationVar(&config.runFor, "run-for", 0, "stop gracefully after this duration; 0 runs until interrupted")
	flags.BoolVar(&config.version, "version", false, "print version and exit")
	flags.BoolVar(&config.monitor.AutoRestart, "auto-restart", false, "enable recovery (default: observe only)")
	flags.BoolVar(&config.monitor.RecoverExited, "recover-exited", false, "recover previously observed containers exiting nonzero")
	flags.DurationVar(&config.monitor.PollInterval, "interval", config.monitor.PollInterval, "per-container polling interval")
	flags.DurationVar(&config.monitor.DiscoveryInterval, "discovery-interval", config.monitor.DiscoveryInterval, "discovery interval")
	flags.DurationVar(&config.monitor.APITimeout, "api-timeout", config.monitor.APITimeout, "timeout per Docker or storage operation")
	flags.DurationVar(&config.monitor.RestartTimeout, "restart-timeout", config.monitor.RestartTimeout, "restart timeout; must exceed 10s")
	flags.DurationVar(&config.monitor.BaseBackoff, "backoff", config.monitor.BaseBackoff, "delay before first recovery attempt")
	flags.DurationVar(&config.monitor.MaxBackoff, "max-backoff", config.monitor.MaxBackoff, "maximum exponential recovery delay")
	flags.DurationVar(&config.monitor.StableReset, "stable-reset", config.monitor.StableReset, "continuous health required to reset retry budget")
	flags.DurationVar(&config.monitor.CrashWindow, "crash-window", config.monitor.CrashWindow, "rolling window for repeated starts")
	flags.IntVar(&config.monitor.CrashThreshold, "crash-threshold", config.monitor.CrashThreshold, "starts within window that constitute a loop")
	flags.IntVar(&config.monitor.MaxRetries, "max-retries", config.monitor.MaxRetries, "recovery attempts allowed until stable reset")
	flags.IntVar(&config.monitor.MaxConcurrent, "concurrency", config.monitor.MaxConcurrent, "maximum concurrent Docker API operations")

	if err := flags.Parse(args); err != nil {
		return config, err
	}
	if flags.NArg() != 0 {
		return config, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if config.version {
		return config, nil
	}
	if err := config.monitor.Validate(); err != nil {
		return config, err
	}
	if config.runFor < 0 {
		return config, fmt.Errorf("run-for cannot be negative")
	}
	if config.database == "" {
		return config, fmt.Errorf("db cannot be empty")
	}
	switch config.output {
	case "auto", "logs", "json", "table":
	default:
		return config, fmt.Errorf("output must be auto, logs, json, or table")
	}
	return config, nil
}
