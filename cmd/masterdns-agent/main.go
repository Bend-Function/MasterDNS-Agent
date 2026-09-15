package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Bend-Function/MasterDNS-Agent/internal/checker"
	"github.com/Bend-Function/MasterDNS-Agent/internal/client"
	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
	"github.com/Bend-Function/MasterDNS-Agent/internal/runner"
	"github.com/Bend-Function/MasterDNS-Agent/internal/spool"
)

var version = "dev"

func main() {
	if err := execute(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, client.ErrUnauthorized) {
		return 3
	}
	return 1
}

func execute(args []string, out io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return executeWithInput(ctx, args, os.Stdin, out)
}

func executeWithInput(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: masterdns-agent <version|enroll|run>")
	}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			return errors.New("version accepts no arguments")
		}
		_, err := fmt.Fprintln(out, version)
		return err
	case "run":
		flags := flag.NewFlagSet("run", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		configPath := flags.String("config", "", "path to agent configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *configPath == "" {
			return errors.New("run requires --config")
		}
		if flags.NArg() != 0 {
			return errors.New("run accepts no positional arguments")
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		return runAgent(ctx, cfg)
	case "enroll":
		flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		configPath := flags.String("config", "", "path to agent configuration")
		installTokenFile := flags.String("install-token-file", "", "path to protected install token file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *configPath == "" {
			return errors.New("enroll requires --config")
		}
		if flags.NArg() != 0 {
			return errors.New("enroll accepts no positional arguments")
		}
		cfg, err := config.LoadForEnrollment(*configPath)
		if err != nil {
			return err
		}
		recovered, err := client.RecoverEnrollment(*configPath, cfg)
		if err != nil {
			return err
		}
		if recovered {
			_, err = fmt.Fprintln(out, "recovered pending enrollment")
			return err
		}
		installToken, err := client.ReadInstallToken(*installTokenFile, in)
		if err != nil {
			return err
		}
		var options []client.Option
		if cfg.CAFile != "" {
			options = append(options, client.WithCAFile(cfg.CAFile))
		}
		platform, err := client.New(cfg.ServerURL, "", options...)
		if err != nil {
			return err
		}
		enrollment, err := platform.Exchange(ctx, installToken)
		if err != nil {
			return err
		}
		if err := client.PersistEnrollment(*configPath, cfg, enrollment); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "enrolled probe %s\n", enrollment.ProbeID)
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAgent(ctx context.Context, cfg config.Config) (err error) {
	token, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return fmt.Errorf("read runtime token: %w", err)
	}
	runtimeToken := strings.TrimSpace(string(token))
	if runtimeToken == "" {
		return errors.New("runtime token file is empty")
	}
	var options []client.Option
	if cfg.CAFile != "" {
		options = append(options, client.WithCAFile(cfg.CAFile))
	}
	platform, err := client.New(cfg.ServerURL, runtimeToken, options...)
	if err != nil {
		return err
	}
	check, err := checker.New(cfg.AllowIPv4, cfg.AllowIPv6, cfg.AllowedPrivateCIDRs, nil)
	if err != nil {
		return err
	}
	disk, err := spool.Open(cfg.StateDir, 0, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, disk.Close()) }()
	run, err := runner.New(platform, check, disk, cfg, nil)
	if err != nil {
		return err
	}
	run.SetAgentVersion(version)
	if err := run.Run(ctx); err != nil {
		if errors.Is(err, client.ErrUnauthorized) {
			return fmt.Errorf("agent authentication rejected; enroll again: %w", err)
		}
		return err
	}
	return nil
}
