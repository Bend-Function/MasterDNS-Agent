package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Bend-Function/MasterDNS-Agent/internal/client"
	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
)

var version = "dev"

func main() {
	if err := execute(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(args []string, out io.Writer) error {
	return executeWithInput(context.Background(), args, os.Stdin, out)
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
		_, err := config.Load(*configPath)
		return err
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
