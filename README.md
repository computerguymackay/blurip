# blurip — automatic DVD and Blu-ray ripping and encoding for Linux, with MakeMKV and HandBrake

[![CI](https://github.com/computerguymackay/blurip/actions/workflows/ci.yml/badge.svg)](https://github.com/computerguymackay/blurip/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/computerguymackay/blurip)](https://github.com/computerguymackay/blurip/releases)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-blue.svg)](LICENSE)

blurip came from turning a large DVD and Blu-ray collection into files
for a home media server. Ripping with [MakeMKV](https://www.makemkv.com/)
and encoding with [HandBrake](https://handbrake.fr/) worked, but choosing
the right titles, renaming them and adding each one to the encode queue
by hand took ages.

**blurip** is a command-line tool for Linux that does that work for you.
It watches your optical drives: insert a disc, and it is read, ripped to
MKV, named and ejected, ready for the next one. Each rip can then be
encoded with HandBrake using the right preset for a DVD or a Blu-ray.
Whole **TV box sets** are ripped episode by episode, named the way
**Plex**, Jellyfin and Emby expect (`Show/Season 01/Show - S01E01.mkv`),
which makes short work of even the biggest box sets. It remembers your
settings and keeps a history of every rip and encode in SQLite.

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
  and ejects it. Discs already ripped are recognised and skipped; if that
  rip's files have since been deleted, blurip asks whether to rip it again
  (a box set disc keeps its episode numbers).
- **Several drives at once**: rip on all drives in parallel, or one at a
  time while the next disc is read and queued. Each drive has a nickname
  ("Blu-ray drive") so you always open the right tray.
- **TV box sets** (`blurip -B`): every episode is ripped, extras and
  "play all" titles are left out, and episode numbers carry on from disc
  to disc, even across restarts.
- **Sensible names**: folders are named after the disc. While a film rips,
  blurip asks whether to change its name ("Change its name? [y/N]"), or,
  if the disc only has a code for a name ("SBK0X7AJ"), asks for a proper
  one; the rip is renamed when you answer. `blurip rename` renames a film
  later.
- **Encoding with HandBrake** (`blurip -Q`, `blurip encode`): finished
  rips are queued and encoded with your own HandBrake presets (one for
  DVDs, one for Blu-rays) into your films and TV folders, then the big
  rips are deleted. Foreign films keep every audio and subtitle track.
- **History**: every rip, skip and failure is recorded with times, sizes
  and the reason it failed (`blurip history`).
- **Safe**: rips go to a temporary folder and only move into place when
  complete; free space is checked first, and a rip never lands on the
  wrong disk because your drive wasn't mounted. The rips disk is never
  filled past `max_fill_percent` (80% to start with): a rip that won't fit
  waits, with the disc in the drive, until you free some space. The films
  and TV disk has its own limit, `encode_max_fill_percent` (the same as
  `max_fill_percent` until you change it), and an encode waits the same
  way (its size estimated from how much your earlier encodes with that
  preset shrank).
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
blurip -s             # same, but ask nothing: saved drive mode, every question's default answer
blurip -B             # TV box-set mode
blurip -Q             # also queue each finished rip for encoding (works with -B)
blurip encode         # encode the queue with HandBrake (-U/--update-list to change the queue first,
                      #   -w/--wait to keep waiting for new rips)
blurip setup          # change settings: pick one from a list (a: go through them all)
blurip once           # rip the disc in the drive, then exit
blurip history        # past rips (-failed, -n N)
blurip encode history # past encodes (-failed, -n N; encode history N for one in full)
blurip config         # show settings; config set KEY VALUE to change one
blurip probe          # show what MakeMKV sees on each disc
blurip drive status   # drive diagnostics (status, watch, eject)
blurip boxset         # box sets and how far each has got (boxset fix to correct one)
blurip rename         # rename a ripped film
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

To rename a film after it has been ripped, stop blurip and run
`blurip rename` (it lists the films ripped most recently) or
`blurip rename "part of its name"`. The rip folder and its files, the
encoded folder and files (subtitles too), the encode queue, the history
and the name blurip remembers for the disc all change together, after it
shows the plan. In a folder shared with other films (encodes made straight
into the films folder), only that film's own files are renamed. Box sets
are renamed with `blurip boxset fix`.

MakeMKV can take several minutes to read a disc (one took nearly 7), and
the screen shows how long it has been going. If it hasn't finished after
20 minutes, the disc is recorded as failed and ejected, so a bad disc can't
hold up the other drives. Change the limit in `blurip setup` or with
`blurip config set read_timeout_minutes 30` (0: never give up).

### Several drives

Each time it starts, blurip lists the drives it found and how many rips
have succeeded or failed so far, then asks how to use the drives: one
drive only, all drives one rip at a time, or all at the same time. Press
Enter to keep last time's choice.

Start with `blurip -s` to be asked nothing at all, for leaving it to get
on with a pile of discs. It uses last time's drive mode, and every
question gets its default answer: films keep their disc's name, a disc
ripped before is skipped, "ask every time" mode rips the main feature,
`-Q`'s foreign-language question is left unanswered (the preset's usual
tracks; change it in `blurip encode -U`), and `-B` rips each disc as its
episodes are detected, without the check, logging any warnings. A box-set
disc with no episodes found is skipped. Only first-time setup still asks.

All drives are watched all the time. A disc you insert while another rips
is read straight away and starts as soon as the current rip ends (or at
once, in parallel mode). Finished discs are ejected immediately so the
next one can go in. A drive that won't open (busy: MakeMKV reading or
ripping another drive can hold it) is tried again every few seconds until
it does, or until you take the disc out by hand; the board says why.

### TV box sets

Start with `blurip -B` (`--box-set`). The show, season and disc number
come from the disc label (`KILLJOYS_S1D1`, `COLUMBO_SEASON6AND7_DISC1`,
"Killjoys: Season One (Disc 1)"), and episodes are named
`Killjoys/Season 01/Killjoys - S01E01.mkv`.

When a label doesn't say the season (`P9053DVD`, `DVD_VIDEO`), blurip
carries on from what it has ripped: the show the disc before was (or,
just started, the box set ripped last), from its next episode, or the
last season ripped of the show the label names. Typing a show's name
under Change suggests its last season and next episode too.

blurip shows you every disc's episodes before it rips (Enter rips them as
shown; you can change the titles, show, season or episode numbers, or
skip the disc), with a warning when something looks off: a disc out of
order, an unusually long title, or a label that covers two seasons. A disc whose
label has no show, season or disc number at all is asked about: if it's a
film, it is ejected without being ripped and the box set's numbering is
left alone (rip films with plain `blurip`). The other way round, if you
put a box-set disc in without `-B` (its titles look like several TV
episodes), blurip asks whether it is a box set and, if so, ejects it
instead of ripping just one title. Two box sets in
two drives keep their own numbering. `blurip history` shows which episodes
each disc became, and `blurip boxset` lists box sets and corrects one
(`blurip boxset set Killjoys 1 next 7`).

#### Every episode in one title

Some discs have no title per episode: the whole disc is one long title,
with chapters marking the episodes (Spaced: 2h50m and 35 chapters for 7
episodes; Angry Kid: 29 minutes and 25 chapters for 25 one-minute
episodes). blurip spots a disc whose only episode-length title has
chapters, and asks how many episodes it holds, listing the counts that
share the chapters out evenly with the length each episode would be:

```
How many episodes are in title 0 (29m32s, 25 chapters)?
  1) 1 episode (don't split it)
  2) 5 episodes of 5 chapters, about 5m54s each
  3) 25 episodes, one chapter each, about 1m11s each
  4) Another number of episodes
```

The default is the count that gives a usual TV episode length (7 × 24
minutes for Spaced), or one episode if none does. It shows each
episode's chapters (episode 1: chapters 1-5, episode 2: chapters 6-10,
...), and you can change them if they don't divide evenly (an intro or
an extra credits chapter). Choosing Change for a disc's episodes asks the
same of any long title with chapters.

The title is ripped once, as `Spaced/Season 01/Spaced - S01E01-E07.mkv`,
and with `-Q` an encode of each episode's chapters is queued: HandBrake
encodes them into `Spaced - S01E01.mp4`, `Spaced - S01E02.mp4` and so on,
each checked against its chapters' length. The rip is deleted (if so set)
once its last episode is encoded. Without `-Q`, add the rip with
`blurip encode -U` and it is split the same way. Numbering carries on
from the last episode in it (the next disc starts at S01E08). Such a disc
can be renamed with its season like any other, but not fixed or ripped
again.

#### Fixing a disc ripped before

Put a box-set disc that was already ripped back in (with `-B`), and
blurip shows its titles, which episode each one became, and whether its
rip and encoded file are still there. Then it asks what to do:

1. **Skip it** (the default; without a terminal it is always skipped).
2. **Rip the whole disc again**, with the same episode numbers. The rips
   are replaced, and with `-Q` each episode is encoded again, replacing
   its earlier encode rather than adding a "(2)" copy.
3. **Fix it**: enter the disc's titles again, in episode order (for
   example `4,1,2` when title 4 is really the first episode and title 3
   was an extra). blurip shows the plan and asks before doing anything:

   ```
   S07E01  title 4  rip now
   S07E02  title 1  rename (was S07E01)
   S07E03  title 2  rename (was S07E02)
   delete  title 3  (was S07E03): its rip, encoded file and queue entry
   Later discs: no change
   ```

   Only titles that weren't ripped before are ripped, and only once they
   have ripped are the other files renamed (rips, encoded files and
   subtitle files beside them, and the encode queue) and the left-out
   titles deleted. If the disc ends up with more or fewer episodes, every
   later episode of the season is renumbered to match. If the rip fails,
   nothing is changed.

To put a disc's episodes in the right order, or delete one (an extra),
without the disc, stop blurip and run `blurip boxset fix SHOW SEASON
[DISC]` (without a disc number it asks which disc). It lists the disc's
titles and their episodes, asks for them in the right order, and makes
the same renames, deletions and renumbering after showing the plan. A
title with nothing left of it (rip and encoded file both deleted) has to
be ripped, which needs the disc.

Fixing a disc (in the drive or not) also checks the numbering either side
of it. If episodes are missing between it and the disc before or after
(disc 1 ends at S08E03 but disc 2 starts at S08E05), it says so and asks
whether to close the gap: the disc and every later one move down to run
on, files and queue entries renamed as above.

It also asks for the disc's first episode (Enter keeps it). A season
started at the wrong number (S01E20 instead of S01E01) is put right by
fixing its first disc with first episode 1: the disc and every later one
move down with it, renamed as above.

While fixing, answer `b` to any question to go back to the first menu;
`boxset fix`'s first menu also has Cancel. Nothing is changed until you
answer yes to "Go ahead?".

A whole season put under the wrong show name or season number can be
renamed: run `blurip boxset fix SHOW SEASON` (without a disc number) and
choose "The show's name or the season number", or choose "Rename the show
or change the season" in the menu for a ripped disc under `-B`. Every
episode's rips, encoded files and subtitle files move to the new
`Show/Season NN` folders with the new names, and the encode queue, the
discs' rip folders in the history and the box set follow. It shows the
plan first, refuses if that show and season already has a box set, and
removes the old folders only if nothing else is left in them.

A disc left in the drive since blurip last handled it (its eject failed,
or ejecting is off) gets no menu when blurip starts again: take it out
and put it back in. Without `-B`, a box-set disc ripped before is skipped.

### Encoding with HandBrake

Rips are big (a Blu-ray is often 30 GB or more). blurip can encode them
with HandBrake into much smaller files for your library, using your own
HandBrake presets.

1. `blurip encode setup` (or just the first `blurip -Q`; later,
   `blurip encode setup` or `blurip setup` change single settings) finds
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
   (`-w` / `--wait` keeps it waiting for new rips). Press **q** to stop after the
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
and records why. A rip still queued for another encode (trying two
presets, say) is kept until the last one; an encode with a different
preset from an earlier one of the same rip goes beside it as " (2)",
while one with the same preset replaces it. Before each encode, its size
is estimated from your earlier encodes (the most the last 20 with the same
preset and disc type shrank to, plus a quarter; half the rip with none to
go by); if that would fill the films or TV disk past `encode_max_fill_percent`,
the encoder waits until space is freed (the entry stays in the queue).

`blurip encode -U` changes the queue before encoding: pick an entry (or
several: `3-5`, `1,4`) and move it (to the top, the bottom or before
another entry), change its tracks (standard or all), preset,
destination or what happens to its rip (kept, deleted, or as set up),
remove it, retry a failed one, or add past rips or any MKV
files by path (you're asked whether each film is foreign, and each show
once). `g` puts the whole queue in order: DVDs first, Blu-rays first, TV
box sets first or films first (each show's episodes kept together), or
back to the order they were added, which is the order it runs in unless
you change it. `-U` works while `blurip encode` is running in another
terminal; the new order is used from its next encode. `blurip encode -L` (`--list`) shows the queue and exits (`-a` with finished
entries). `blurip encode -S` (`--stats`) shows what has been encoded: how
many films and episodes, data in and out (and the share it shrank to),
space freed by deleted rips, time spent encoding and speed against real
time, split by disc type and by preset. Speed needs each rip's running
time, which is recorded for encodes from this version on.
`blurip encode history` lists each encode, latest first: when it
finished, how long it took, the title, disc type, preset, size before and
after, speed, and where it went (or why it failed); `-failed` for failures
only, `-n N` for more or fewer. `blurip encode history N` shows everything
about one: the rip (kept or deleted) and the rip job it came from, sizes
and saving, preset and tracks, running time, and how long it waited in the
queue and took to encode. A HandBrake Flatpak is checked for access to your folders, and
offered the same fix as MakeMKV.

### Leaving it running overnight

While a disc is being read or ripped, or an encode is running, blurip stops
the PC going to sleep. It asks the desktop (KDE, GNOME and others, through
the XDG desktop portal) not to suspend, and also takes a systemd-logind lock
for a PC without a desktop (`systemd-inhibit --list` shows it). When
there's nothing to do, even while `blurip` watches the drives or
`blurip encode -w` waits for rips, the PC sleeps as usual. If the desktop
refuses, blurip says so on screen and carries on.

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
`$BLURIP_DB`). Each disc's own label and name are kept whatever you
rename it to, along with MakeMKV's full description of it (every
title's length, size, segments and streams, from the latest read).

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
