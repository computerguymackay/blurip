# blurip — automatic DVD and Blu-ray ripping for Linux, with MakeMKV

[![CI](https://github.com/computerguymackay/blurip/actions/workflows/ci.yml/badge.svg)](https://github.com/computerguymackay/blurip/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/computerguymackay/blurip)](https://github.com/computerguymackay/blurip/releases)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-blue.svg)](LICENSE)

**blurip** is a command-line tool for Linux that watches your optical
drives and automatically rips every DVD and Blu-ray you put in, using
[MakeMKV](https://www.makemkv.com/). Insert a disc, and it is read,
ripped to MKV, named and ejected, ready for the next one. It remembers
your settings, keeps a history of every rip in SQLite, and can rip whole
**TV box sets** with episodes named the way **Plex**, Jellyfin and Emby
expect (`Show/Season 01/Show - S01E01.mkv`).

It runs in your terminal while you use it, with a live progress panel. It
is not a background service.

```
blurip · watching DVD drive (sr0), Blu-ray drive (sr1) (one rip at a time) · Ctrl-C to stop
DVD drive (sr0)      The Predator
                       Ready — rips next
Blu-ray drive (sr1)  Wild Card → /media/rips/Wild Card/Wild Card.mkv
                       Saving to MKV file [██████░░░░░░░░░░░░░░]  31.0%  ETA 33m10s
```

## Features

- **Hands-free ripping**: insert a disc, and blurip rips the main feature
  and ejects it. Discs already ripped are recognised and skipped.
- **Several drives at once**: rip on all drives in parallel, or one at a
  time while the next disc is read and queued. Each drive has a nickname
  ("Blu-ray drive") so you always open the right tray.
- **TV box sets** (`blurip -B`): every episode is ripped, extras and
  "play all" titles are left out, and episode numbers carry on from disc
  to disc, even across restarts.
- **Sensible names**: folders are named after the disc. If the disc only
  has a code for a name ("SBK0X7AJ"), blurip asks for a proper name while
  it rips, and renames the rip when you answer.
- **Encoding with HandBrake** (`blurip -Q`, `blurip encode`): finished
  rips are queued and encoded with your own HandBrake presets (one for
  DVDs, one for Blu-rays) into your films and TV folders, then the big
  rips are deleted. Foreign films keep every audio and subtitle track.
- **History**: every rip, skip and failure is recorded with times, sizes
  and the reason it failed (`blurip history`).
- **Safe**: rips go to a temporary folder and only move into place when
  complete; free space is checked first, and a rip never lands on the
  wrong disk because your drive wasn't mounted.
- **Works with Flatpak MakeMKV** (Bazzite, Fedora Silverblue and similar),
  including giving it access to your output folder.
- **One static binary**, no dependencies other than MakeMKV.

## Requirements

- Linux (x86-64 or ARM64). blurip is Linux-only; Windows and macOS
  versions would be welcome as contributions (disc detection, eject and a
  few system calls are the Linux-specific parts).
- [MakeMKV](https://www.makemkv.com/) (`makemkvcon`), installed natively
  or as the [Flatpak](https://flathub.org/apps/com.makemkv.MakeMKV).
  MakeMKV is free while in beta; see its site for the current key.
- A DVD or Blu-ray drive. For Blu-ray, a drive MakeMKV supports.
- Optional, for encoding: [HandBrake](https://handbrake.fr/)'s
  `HandBrakeCLI`, installed natively or as the
  [Flatpak](https://flathub.org/apps/fr.handbrake.ghb) (which includes it).

## Install

Download the binary for your machine from the
[releases page](https://github.com/computerguymackay/blurip/releases),
then:

```sh
chmod +x blurip-*-linux-amd64
sudo mv blurip-*-linux-amd64 /usr/local/bin/blurip
```

Or with Go 1.27 or later:

```sh
go install github.com/computerguymackay/blurip@latest
```

## Quick start

```sh
blurip
```

The first run asks a few questions: where MakeMKV is (usually found
automatically), which drives to use and what to call them, and where to
save rips. Then it watches the drives. Put a disc in, and press Ctrl-C to
stop when you are done.

## Usage

```sh
blurip                # watch the drives and rip until Ctrl-C (setup on first run)
blurip -s             # same, without asking how to use the drives this time
blurip -B             # TV box-set mode
blurip -Q             # also queue each finished rip for encoding (works with -B)
blurip encode         # encode the queue with HandBrake (-U to change the queue first)
blurip setup          # run setup again
blurip once           # rip the disc in the drive, then exit
blurip history        # past rips (-failed, -n N)
blurip config         # show settings; config set KEY VALUE to change one
blurip probe          # show what MakeMKV sees on each disc
blurip drive status   # drive diagnostics (status, watch, eject)
blurip boxset         # box sets and how far each has got
blurip version
```

### Where rips go

By default the main feature (the largest title) is ripped to
`<output>/<Disc Name>/<Disc Name>.mkv`. With separate folders turned on
(`blurip config set split_by_type yes`, or in setup), DVDs go to
`<output>/DVD/...` and Blu-rays to `<output>/Blu-ray/...`, which is
handy if you process them differently afterwards.

In "ask every time" mode (`blurip config set rip_mode ask`) you see every
title on the disc (length, size, audio and subtitle languages) and choose
what to rip and what to call it.

### Several drives

Each time it starts, blurip lists the drives it found and how many rips
have succeeded or failed so far, then asks how to use the drives: one
drive only, all drives one rip at a time, or all at the same time. Press
Enter to keep last time's choice, or start with `blurip -s` to skip the
question.

All drives are watched all the time. A disc you insert while another rips
is read straight away and starts as soon as the current rip ends (or at
once, in parallel mode). Finished discs are ejected immediately so the
next one can go in.

### TV box sets

Start with `blurip -B` (`--box-set`). The show, season and disc number
come from the disc label (`KILLJOYS_S1D1`, `COLUMBO_SEASON6AND7_DISC1`,
"Killjoys: Season One (Disc 1)"), and episodes are named
`Killjoys/Season 01/Killjoys - S01E01.mkv`.

blurip shows you the first disc's episodes to confirm, and asks again for
a new season or when something looks off: a disc out of order, an
unusually long title, or a label that covers two seasons. A disc whose
label has no show, season or disc number at all is asked about: if it's a
film, it is ejected without being ripped and the box set's numbering is
left alone (rip films with plain `blurip`). The other way round, if you
put a box-set disc in without `-B` (its titles look like several TV
episodes), blurip asks whether it is a box set and, if so, ejects it
instead of ripping just one title. Two box sets in
two drives keep their own numbering. `blurip history` shows which episodes
each disc became, and `blurip boxset` lists box sets and corrects one
(`blurip boxset set Killjoys 1 next 7`).

### Encoding with HandBrake

Rips are big (a Blu-ray is often 30 GB or more). blurip can encode them
with HandBrake into much smaller files for your library, using your own
HandBrake presets.

1. `blurip encode setup` (or just the first `blurip -Q`) finds
   HandBrakeCLI and asks for a preset for DVDs and one for Blu-rays,
   chosen from menus: first the category (General, Web, Devices,
   Matroska, Hardware, Professional, and your own presets from the
   HandBrake app), then the preset. It also asks for a films folder, a TV
   folder, and whether to delete each rip once its encode is checked.
2. Rip with `blurip -Q`. Each finished rip is added to the encode queue,
   and while the disc rips you are asked whether it is a foreign film
   ("Is Ip Man a foreign film? [y/N]"). In box-set mode (`-B -Q`) this is
   asked once per show.
3. Run `blurip encode`, in another terminal at the same time if you like
   (`-w` keeps it waiting for new rips). Press **q** to stop after the
   current encode, or Ctrl-C to stop at once; run it again to carry on.

Films go to `<films folder>/Name/Name.mp4`, box-set episodes to
`<TV folder>/Show/Season 01/Show - S01E01.mp4`. To get the year in a
film's name, name the disc that way ("Ip Man (2008)") in ask mode or when
blurip asks for a name. Normal encodes use your preset as it is (often
English audio and forced subtitles). **Foreign films** keep every audio
and subtitle track as tracks you can switch on. From a Blu-ray they are
saved as MKV, because MP4 can't hold Blu-ray subtitles except burned into
the picture; DVDs keep your preset's format (MP4 can hold DVD subtitles).

Each encode is written to a temporary folder, checked (HandBrake
succeeded and the encode is as long as the rip), and only then moved into
place; only after that is the rip deleted. A failed encode keeps the rip
and records why.

`blurip encode -U` changes the queue before encoding: pick an entry (or
several: `3-5`, `1,4`) and change its tracks (standard or all), preset or
destination, remove it, retry a failed one, or add past rips or any MKV
files by path (you're asked whether each film is foreign, and each show
once). `blurip encode list` shows the queue (`-a` with finished
entries). A HandBrake Flatpak is checked for access to your folders, and
offered the same fix as MakeMKV.

### MakeMKV as a Flatpak

If MakeMKV is installed as a Flatpak, setup finds it and runs
`flatpak run --command=makemkvcon com.makemkv.MakeMKV`. A wrapper script
works too (it needs a `#!/bin/bash` first line and `chmod +x`), as does
`blurip config set makemkvcon_path "<command>"`.

A Flatpak can only use folders it has been given, and anything it writes
elsewhere is lost when it exits. blurip checks at startup that MakeMKV can
write to your output folder and, if not, offers to allow it by running
`flatpak override --user --filesystem=<folder> com.makemkv.MakeMKV`
(no root needed).

### Settings and data

Settings, drives, box-set progress, the rip history and the encode queue live in one SQLite
database, `~/.config/blurip/blurip.db` (change with `-db` or
`$BLURIP_DB`).

## Troubleshooting

- **"makemkvcon finished but produced no MKV file"**: the error includes
  MakeMKV's last messages. With a Flatpak MakeMKV it usually means the
  Flatpak can't use the output folder; restart blurip and accept its
  offer to fix it.
- **Missing audio or subtitle tracks**: blurip uses MakeMKV's own track
  selection. To keep every track, set MakeMKV's "Default selection rule"
  to `+sel:all` in the MakeMKV app's preferences.
- **The disc isn't detected**: `blurip drive status` and
  `blurip drive watch` show what the drive reports; `blurip probe` shows
  what MakeMKV sees.

## Building from source

```sh
make build                     # ./blurip (static, no cgo)
make test                      # go vet + go test
make release                   # dist/ binaries for amd64 and arm64 + SHA256SUMS
make deploy RIPPER=user@host   # build and copy to another machine
```

`make deploy` reads `RIPPER` from the command line, the environment, or an
optional untracked `local.mk`. Pushing a tag such as `v1.0.0` builds the
release binaries and publishes them on GitHub. See [PLAN.md](PLAN.md) for
the design.

## Legal

blurip is not affiliated with or endorsed by MakeMKV or GuinpinSoft. It
only drives MakeMKV, which you install and license yourself. Only rip
discs you own, and check the law where you live: copying discs, even for
personal backup, is not legal everywhere.

## License

Copyright © 2026 Colin Mackay. blurip is free software, licensed under the
[GNU General Public License v3.0](LICENSE).
