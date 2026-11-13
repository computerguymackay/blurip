# blurip

A Linux command-line tool that watches your optical drives and rips each
DVD or Blu-ray you insert with [MakeMKV](https://www.makemkv.com/),
remembering your settings and keeping a history of every job (in SQLite).

It runs in a terminal while you use it — it is not a service.

## Install

You need MakeMKV (`makemkvcon`). blurip itself is one static binary:

```sh
make build            # ./blurip
make deploy           # build and scp to the ripping PC (RIPPER=user@host)
make release          # dist/ binaries for amd64 and arm64 + SHA256SUMS
```

## Use

```sh
blurip                # first run: setup; then watch the drives and rip until Ctrl-C
blurip setup          # run setup again
blurip once           # rip the disc in the drive, then exit
blurip history        # past rips (-failed, -n N)
blurip config         # show settings; config set KEY VALUE to change one
blurip probe          # show what MakeMKV sees on each disc
blurip drive status   # drive diagnostics (status, watch, eject)
```

By default the main feature (the largest title) is ripped to
`<output>/<Disc Name>/<Disc Name>.mkv`. With separate folders turned on
(`blurip config set split_by_type yes`, or in setup), DVDs go to
`<output>/DVD/...` and Blu-rays to `<output>/Blu-ray/...`.

Each time it starts, blurip lists the drives it found and how many rips
have succeeded or failed so far, then asks how to use the drives this time:
one drive only (and which), all drives one rip at a time, or all at the
same time. Press Enter to keep last time's choice, or start with
`blurip -s` (`--skip`) to skip the question and go straight to watching
with last time's choice.

With several drives, each gets a nickname in setup ("Blu-ray drive",
"DVD drive", ...) that is shown with every message, so you always open the
right tray. All drives are watched all the time: a disc you insert while
another rips is read straight away and starts as soon as the current rip
ends (or at the same time, in parallel mode). Finished discs are ejected
immediately so the next one can go in.

A disc that was ripped successfully before is skipped. In "ask every time"
mode (`rip_mode ask`) you see every title and choose what to rip.

The database lives at `~/.config/blurip/blurip.db` (change with `-db` or
`$BLURIP_DB`). See [PLAN.md](PLAN.md) for the design.
