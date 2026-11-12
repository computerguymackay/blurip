// Command blurip watches optical drives and rips discs with MakeMKV.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/setup"
	"github.com/computerguymackay/blurip/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "blurip:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("blurip", flag.ContinueOnError)
	dbPath := global.String("db", "", "database file (default $"+store.EnvDB+" or ~/.config/blurip/blurip.db)")
	global.Usage = usage(global)
	if err := global.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" {
		p, err := store.DefaultPath()
		if err != nil {
			return err
		}
		*dbPath = p
	}

	cmd, rest := "", global.Args()
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}

	// Ctrl-C keeps its default behaviour (exit immediately): the wizard
	// blocks on terminal input and only writes once, at the end.
	ctx := context.Background()

	switch cmd {
	case "":
		return runMain(ctx, *dbPath)
	case "setup":
		return runSetup(ctx, *dbPath)
	case "config":
		return runConfig(ctx, *dbPath)
	case "probe":
		return runProbe(rest)
	case "help":
		global.Usage()
		return nil
	}
	global.Usage()
	return fmt.Errorf("unknown command %q", cmd)
}

func usage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprint(fs.Output(), `Usage: blurip [-db file] [command]

With no command, blurip runs first-time setup if needed and then watches
the drives.

Commands:
  setup    Run the setup wizard again
  config   Show the saved settings and drives
  probe    Scan drives and show what MakeMKV sees on each disc

Options:
`)
		fs.PrintDefaults()
	}
}

func openStore(ctx context.Context, path string) (*store.Store, config.Config, error) {
	s, err := store.Open(ctx, path)
	if err != nil {
		return nil, config.Config{}, err
	}
	cfg, err := config.Load(ctx, s)
	if err != nil {
		s.Close()
		return nil, config.Config{}, err
	}
	return s, cfg, nil
}

func runMain(ctx context.Context, dbPath string) error {
	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	if !cfg.SetupComplete {
		fmt.Println("No settings found; starting first-time setup.")
		fmt.Println()
		if cfg, err = setupAndSave(ctx, s, cfg); err != nil {
			return err
		}
	}
	fmt.Println("Watching drives is not implemented yet.")
	return nil
}

func runSetup(ctx context.Context, dbPath string) error {
	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = setupAndSave(ctx, s, cfg)
	return err
}

func setupAndSave(ctx context.Context, s *store.Store, cur config.Config) (config.Config, error) {
	p := setup.NewPrompter(os.Stdin, os.Stdout)
	res, err := setup.Run(ctx, p, setup.DefaultEnv(), cur)
	if err != nil {
		return cur, err
	}
	if err := setup.Save(ctx, s, res); err != nil {
		return cur, err
	}
	fmt.Printf("\nSettings saved to %s\n", s.Path())
	return res.Config, nil
}

func runConfig(ctx context.Context, dbPath string) error {
	s, _, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()

	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Database: %s (schema v%d)\n\n", s.Path(), version)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SETTING\tVALUE")
	defaults := config.Default().ToMap()
	for _, k := range config.Keys() {
		v, ok := settings[k]
		if !ok {
			v = defaults[k] + "  (default)"
		}
		fmt.Fprintf(tw, "%s\t%s\n", k, dash(v))
	}
	tw.Flush()

	drives, err := s.Drives(ctx)
	if err != nil {
		return err
	}
	fmt.Println()
	if len(drives) == 0 {
		fmt.Println("No drives configured.")
		return nil
	}
	tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DRIVE\tENABLED\tNAME")
	for _, d := range drives {
		fmt.Fprintf(tw, "%s\t%t\t%s\n", d.Device, d.Enabled, d.Name)
	}
	return tw.Flush()
}
