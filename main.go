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

	"github.com/computerguymackay/blurip/internal/awake"
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
	var skip bool
	global.BoolVar(&skip, "skip", false, "skip the startup question and use the drive mode chosen last time")
	global.BoolVar(&skip, "s", false, "short for -skip")
	var boxSet bool
	global.BoolVar(&boxSet, "box-set", false, "rip TV box sets: every episode on each disc, named Show - S01E01")
	global.BoolVar(&boxSet, "B", false, "short for -box-set")
	var queue bool
	global.BoolVar(&queue, "queue", false, "add each finished rip to the HandBrake encode queue (see blurip encode)")
	global.BoolVar(&queue, "Q", false, "short for -queue")
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

	if (skip || boxSet || queue) && cmd != "" {
		return errors.New("-skip, -box-set and -queue only apply when running blurip with no command")
	}
	switch cmd {
	case "":
		return runMain(ctx, *dbPath, skip, boxSet, queue)
	case "encode":
		return runEncode(ctx, *dbPath, rest)
	case "setup":
		return runSetup(ctx, *dbPath)
	case "config":
		return runConfig(ctx, *dbPath, rest)
	case "once":
		return runOnce(*dbPath)
	case "history":
		return runHistory(ctx, *dbPath, rest)
	case "probe":
		return runProbe(ctx, *dbPath, rest)
	case "drive":
		return runDrive(*dbPath, rest)
	case "boxset":
		return runBoxSet(ctx, *dbPath, rest)
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
		fmt.Fprint(fs.Output(), `Usage: blurip [-db file] [-s|--skip] [-B|--box-set] [-Q|--queue] [command]

With no command, blurip runs first-time setup if needed, then watches the
drives and rips each disc that is inserted. Press Ctrl-C to stop.
With -s (--skip) it doesn't ask how to use the drives and goes straight to
watching, using the drive mode chosen last time.
With -B (--box-set) every disc is ripped as part of a TV box set: all the
episodes, named Show/Season 01/Show - S01E01.mkv, carrying on from one
disc to the next. You check each disc's episodes before it rips.
Put in a box set disc that was already ripped and -B asks whether to skip
it, rip it all again, or fix it (reorder episodes, drop an extra, rip a
missed episode). Without the disc, use blurip boxset fix.
With -Q (--queue) every finished rip is added to the encode queue, and you
are asked whether each film is foreign (keep every audio and subtitle
track); run blurip encode to encode the queue with HandBrake.

Commands:
  setup    Change settings: pick one from a list, or go through them all
  once     Rip the main feature of the disc in the drive, then exit
  history  Show past rips (-failed for failures only, -n to limit)
  config   Show or change settings: config [get KEY | set KEY VALUE | drive DEVICE on|off]
  probe    Scan drives and show what MakeMKV sees on each disc
  drive    Check drive status, watch for discs, or eject
  boxset   List box sets and how far each has got; correct, fix or forget one
           (boxset fix SHOW SEASON [DISC] reorders a disc's episodes, drops one,
           or closes a gap in the numbering)
  encode   Encode the queued rips with HandBrake: encode [-U|--update-list] [-w|--wait]
           (-U changes the queue first; -w waits for more rips when it is empty;
           encode -L|--list shows the queue, encode -S|--stats what's been
           encoded; both then exit)
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

func runMain(ctx context.Context, dbPath string, skip, boxSet, queue bool) error {
	if boxSet && !isTerminal(os.Stdin) {
		return errors.New("box-set mode asks you to check each disc, so it needs a terminal")
	}
	s, cfg, cleanup, err := openForRipping(ctx, dbPath, os.Stdout)
	if err != nil {
		return err
	}
	defer cleanup()

	// One prompter for everything: each reads stdin through a buffer.
	p := prompt.NewPrompter(os.Stdin, os.Stdout)
	ask := isTerminal(os.Stdin) && !skip
	if !cfg.SetupComplete {
		fmt.Println("No settings found; starting first-time setup.")
		fmt.Println()
		if cfg, err = setupAndSave(ctx, s, cfg, p); err != nil {
			return err
		}
		fmt.Println()
		ask = false // the drive mode was just chosen
	}
	if queue {
		if cfg, err = ensureEncodeSetup(ctx, s, cfg, p, isTerminal(os.Stdin)); err != nil {
			return err
		}
	}

	ctl := drive.CDROM{UseEjectCommand: true}
	startAsk := p
	if !ask {
		startAsk = nil
	}
	cfg, devices, labels, err := prepareStart(ctx, s, cfg, os.Stdout, startAsk, ctl)
	if err != nil {
		return err
	}
	var sandboxAsk *prompt.Prompter
	if isTerminal(os.Stdin) {
		sandboxAsk = p
	}
	if err := checkSandbox(ctx, cfg, os.Stdout, sandboxAsk); err != nil {
		return err
	}

	// From here on Ctrl-C stops blurip cleanly instead of killing it.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	w := &watcher{
		cfg: cfg, store: s, ctl: ctl, devices: devices, labels: labels,
		interval: 2 * time.Second, out: os.Stdout, tty: isTerminal(os.Stdout), now: time.Now,
		ask: p.WithContext(ctx), canAsk: isTerminal(os.Stdin),
		awake: awake.New("Ripping discs", nil),
	}
	if boxSet {
		w.boxSet = &boxSetState{}
	}
	w.queue = queue
	return w.run(ctx)
}

func runSetup(ctx context.Context, dbPath string) error {
	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	p := prompt.NewPrompter(os.Stdin, os.Stdout)
	if cfg.SetupComplete && isTerminal(os.Stdin) {
		return settingsMenu(ctx, s, cfg, p, false)
	}
	_, err = setupAndSave(ctx, s, cfg, p)
	return err
}

// setupEnv is the setup environment, offering the saved drive nicknames,
// and the drives in use now.
func setupEnv(ctx context.Context, s *store.Store) (setup.Env, []store.Drive) {
	env := setup.DefaultEnv()
	var enabled []store.Drive
	if drives, err := s.Drives(ctx); err == nil {
		env.Nicknames = map[string]string{}
		for _, d := range drives {
			env.Nicknames[d.Device] = d.Nickname
			if d.Enabled {
				enabled = append(enabled, d)
			}
		}
	}
	return env, enabled
}

// settingsMenu is `blurip setup` (or, with encodeOnly, `blurip encode
// setup`) once blurip is set up: change single settings from a list.
func settingsMenu(ctx context.Context, s *store.Store, cur config.Config, p *prompt.Prompter, encodeOnly bool) error {
	env, drives := setupEnv(ctx, s)
	res, changed, err := setup.Menu(ctx, p, env, setup.DefaultEncodeEnv(), cur, drives, encodeOnly)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println("No changes.")
		return nil
	}
	if err := setup.Save(ctx, s, res); err != nil {
		return err
	}
	fmt.Printf("Settings saved to %s\n", s.Path())
	noteIfRunning(s.Path())
	return nil
}

func setupAndSave(ctx context.Context, s *store.Store, cur config.Config, p *prompt.Prompter) (config.Config, error) {
	env, _ := setupEnv(ctx, s)
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
