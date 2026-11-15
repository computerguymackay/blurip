# blurip

A Linux command-line tool that watches your optical drives and rips each
DVD or Blu-ray you insert with [MakeMKV](https://www.makemkv.com/),
remembering your settings and keeping a history of every job (in SQLite).

It runs in a terminal while you use it — it is not a service.

## Install

You need MakeMKV (`makemkvcon`). If MakeMKV is installed as a Flatpak
(Bazzite, Silverblue, ...), setup finds it and runs
`flatpak run --command=makemkvcon com.makemkv.MakeMKV`; a wrapper script
also works (it needs a `#!/bin/bash` first line and `chmod +x`), and so
does `blurip config set makemkvcon_path "<command>"`.
blurip itself is one static binary:

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
blurip boxset         # box sets and how far each has got
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

For TV box sets, start with `blurip -B` (`--box-set`). Every episode on
each disc is ripped (extras and "play all" titles are left out) and named
the way Plex expects: `Friends/Season 01/Friends - S01E01.mkv`. The show,
season and disc number come from the disc label, and episode numbers carry
on from one disc to the next, even after you quit and restart blurip.
blurip shows you the first disc's episodes to confirm (with where a season
had got to, if you've ripped some of it before), and asks again for a new
season or when something looks off (a disc out of order, an unusually long
title). Two box sets in two drives keep their own numbering.
`blurip history` shows which episodes each disc became; `blurip boxset`
lists box sets and corrects one (`blurip boxset set Friends 1 next 7`).

If a disc's name looks like a code rather than a title ("SBK0X7AJ",
"DVD_VIDEO"), blurip asks what it is called, without holding anything up:
the disc rips under the code name meanwhile, and your answer is used when
the rip is moved into place, or renames it if it already has been.
Questions from several discs wait their turn. The name is remembered for
that disc.

A disc that was ripped successfully before is skipped. In "ask every time"
mode (`rip_mode ask`) you see every title and choose what to rip.

The database lives at `~/.config/blurip/blurip.db` (change with `-db` or
`$BLURIP_DB`). See [PLAN.md](PLAN.md) for the design.
