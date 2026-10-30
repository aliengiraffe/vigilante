package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// runStateCommand handles `vigilante state <subcommand>`. The only subcommand
// is `export`, which writes the database back out in the pre-SQLite JSON
// shapes so operators keep a human-readable view and can downgrade.
func (a *App) runStateCommand(_ context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		a.printStateUsage(a.stdout)
		if len(args) == 0 {
			return errors.New("usage: vigilante state export [--dir <path>]")
		}
		return nil
	}
	switch args[0] {
	case "export":
		return a.runStateExportCommand(args[1:])
	default:
		return fmt.Errorf("unknown state subcommand %q (expected: export)", args[0])
	}
}

func (a *App) printStateUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: vigilante state export [--dir <path>]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Export the state database as the legacy JSON files.")
	fmt.Fprintln(w, "Without --dir, one JSON object with watchlist, sessions, config, and hardening_state is written to stdout.")
	fmt.Fprintln(w, "With --dir, watchlist.json, sessions.json, config.json, and hardening_state.json are written into that directory.")
}

func (a *App) runStateExportCommand(args []string) error {
	fs := flag.NewFlagSet("state export", flag.ContinueOnError)
	configureFlagSet(fs, func(w io.Writer) {
		a.printStateUsage(w)
		fmt.Fprintln(w)
		fs.SetOutput(w)
		fs.PrintDefaults()
	})
	dir := fs.String("dir", "", "directory to write the legacy JSON files into")
	if err := parseFlagSet(fs, args, a.stdout); err != nil {
		if errors.Is(err, errHelpHandled) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: vigilante state export [--dir <path>]")
	}
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	if target := strings.TrimSpace(*dir); target != "" {
		if err := a.state.WriteExport(target); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "exported state to %s\n", target)
		return nil
	}
	export, err := a.state.Export()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(a.stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(export)
}
