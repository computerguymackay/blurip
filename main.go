// Command blurip watches optical drives and rips discs with MakeMKV.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/setup"
	"github.com/computerguymackay/blurip/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		if errors.Is(err, errInterrupted) {
			os.Exit(130)
		}
		if errors.Is(err, errRipFailed) {
			os.Exit(1) // reason already printed
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
		return runConfig(ctx, *dbPath, rest)
	case "once":
		return runOnce(*dbPath)
	case "history":
		return runHistory(ctx, *dbPath, rest)
	case "probe":
		return runProbe(rest)
	case "drive":
		return runDrive(*dbPath, rest)
	case "version", "-version", "--version":
		fmt.Println(versionString())
		return nil
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

With no command, blurip runs first-time setup if needed, then watches the
drives and rips each disc that is inserted. Press Ctrl-C to stop.

Commands:
  setup    Run the setup wizard again
  once     Rip the main feature of the disc in the drive, then exit
  history  Show past rips (-failed for failures only, -n to limit)
  config   Show or change settings: config [get KEY | set KEY VALUE | drive DEVICE on|off]
  probe    Scan drives and show what MakeMKV sees on each disc
  drive    Check drive status, watch for discs, or eject
  version  Show the blurip version

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
	s, cfg, cleanup, err := openForRipping(ctx, dbPath, os.Stdout)
	if err != nil {
		return err
	}
	defer cleanup()

	if !cfg.SetupComplete {
		fmt.Println("No settings found; starting first-time setup.")
		fmt.Println()
		if cfg, err = setupAndSave(ctx, s, cfg); err != nil {
			return err
		}
	}
	devices, labels, err := enabledDrives(ctx, s)
	if err != nil {
		return err
	}

	// From here on Ctrl-C stops blurip cleanly instead of killing it.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	w := &watcher{
		cfg: cfg, store: s, ctl: drive.CDROM{UseEjectCommand: true}, devices: devices, labels: labels,
		interval: 2 * time.Second, out: os.Stdout, tty: isTerminal(os.Stdout), now: time.Now,
		ask: prompt.NewPrompter(os.Stdin, os.Stdout).WithContext(ctx),
	}
	return w.run(ctx)
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
	p := prompt.NewPrompter(os.Stdin, os.Stdout)
	env := setup.DefaultEnv()
	if drives, err := s.Drives(ctx); err == nil {
		env.Nicknames = map[string]string{}
		for _, d := range drives {
			env.Nicknames[d.Device] = d.Nickname
		}
	}
	res, err := setup.Run(ctx, p, env, cur)
	if err != nil {
		return cur, err
	}
	if err := setup.Save(ctx, s, res); err != nil {
		return cur, err
	}
	fmt.Printf("\nSettings saved to %s\n", s.Path())
	return res.Config, nil
}
