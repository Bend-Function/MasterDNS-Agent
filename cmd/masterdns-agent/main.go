package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
	if len(args) == 0 {
		return errors.New("usage: masterdns-agent <version|run>")
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
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
