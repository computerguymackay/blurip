# blurip — Plan

A self-contained Linux command-line tool that watches one or more optical
drives and automatically rips DVDs and Blu-rays with MakeMKV, remembering
settings and keeping a full history of every job.

## Decisions

| Topic | Decision |
|---|---|
| Platform | Linux only (for now) |
| Run model | Interactive CLI run in a terminal. **Never** a service/daemon. |
| Discs | DVD and Blu-ray |
| Packaging | Single static binary (pure-Go SQLite, no cgo). Only external dependency is MakeMKV (`makemkvcon`). |
| State | SQLite file, default `~/.config/blurip/blurip.db`, override with `--db` / `BLURIP_DB` |
| MakeMKV interface | `makemkvcon -r` (robot mode) for disc info and progress; drives addressed as `dev:/dev/srN` |
| Track selection | **For now:** rip the main feature with all audio tracks, using MakeMKV's own track selection (no `--profile`). **Later:** per-job temporary `--profile=<file>` containing the selection rule, once a MakeMKV profile file to base it on is available (none is installed on the ripping PC). |
| Default title | Main feature = largest title by size; ties broken by longest duration |
| Previously ripped | Disc with a prior **successful** rip is skipped (ejected, recorded as `skipped`). In "ask every time" mode, the user is told when it was ripped and asked whether to rip again. Failed/interrupted discs rip normally. |
| Output folder | MakeMKV's disc name if it has one (`Wild Card`), otherwise the cleaned-up volume label (`THE_MATRIX` → `The Matrix`); editable in ask mode. A single title is saved as `<Name>/<Name>.mkv`. |
| Partial rips | Rip into `<output>/.blurip-tmp/<job-id>/`, rename into place on success |

## Drive modes

| Mode | Behaviour |
|---|---|
| `single` (default) | One configured drive |
| `sequential` | Watch all enabled drives; rip one disc at a time. All monitoring pauses during a rip; afterwards all drives are rescanned and the next disc is taken. |
| `parallel` | One worker per drive; monitoring pauses only for the drive that is ripping. Optional `max_concurrent`. |

Parallel-mode implications:

- Space check subtracts the estimated size of every in-flight rip ("reservations").
- Ask-every-time prompts are serialised: one prompt at a time; other rips keep going.
- Progress display: one updating line per active drive.
- All DB writes go through a single writer (WAL mode).
- Ctrl-C stops every in-flight rip, cleans each up and marks each `interrupted`.

## Flow

1. Watch drive(s) (cheap `ioctl(CDROM_DRIVE_STATUS)` polling; not `makemkvcon`).
2. Disc detected → `makemkvcon -r info dev:/dev/srN` → fingerprint (volume label + per-title duration/size hash).
3. Look up history → skip / ask / proceed.
4. Choose titles and tracks (defaults or interactive), optionally saving choices as new defaults.
5. Space check against estimated size and the configured fill threshold (e.g. 80%) → refuse if no room.
6. Rip with live progress into the temp folder; rename on success.
7. Record the job (start, finish, status, failure reason, output path, bytes).
8. Eject, confirm the disc is gone, resume watching.

On startup, any job still marked `running` is marked `interrupted` and its temp folder removed.

## Commands

| Command | Purpose |
|---|---|
| `blurip` | Run (first-run setup, then watch) |
| `blurip setup` | Re-run the setup wizard |
| `blurip config get\|set` | View or change settings |
| `blurip history [--failed]` | Job history |
| `blurip once` | Rip the current disc and exit |
| `blurip drive status\|watch\|eject` | Diagnostic: drive status, watch for discs, eject and confirm |
| `blurip probe` | Diagnostic: scan drives, print parsed disc info and the chosen main feature, optionally save raw `makemkvcon` output |

## Schema (initial)

- `settings(key, value)`
- `drives(id, device, name, enabled)`
- `discs(id, fingerprint, name, volume_label, disc_type, first_seen)`
- `jobs(id, disc_id, drive_id, started_at, finished_at, status, failure_reason, output_path, bytes_written)`
  — status: `running | succeeded | failed | interrupted | skipped`
- `job_titles(job_id, title_id, audio_selection, subtitle_selection)`

Migrations are embedded SQL files applied on startup.

## Build steps

Development happens on a machine with no optical drive; real-hardware checks
happen on the ripping PC.

1. ✅ **makemkv robot-output parser + `blurip probe`** — parse `MSG`/`DRV`/`CINFO`/`TINFO`/`SINFO`/`TCOUNT`, main-feature selection. Verified on the ripping PC (MakeMKV 2.0.0, Blu-ray in `/dev/sr1`).
2. ✅ **Foundation** — DB, migrations, settings, MakeMKV discovery, setup wizard (`blurip setup`, `blurip config`). Audio/subtitle questions are left out of the wizard until track selection via `--profile` exists.
3. 🔶 **Drive layer** — detection via ioctl, eject (falls back to the `eject` program if the disc is auto-mounted), confirm removal, ignore a handled disc until it is removed; `blurip drive status|watch|eject`. Built and unit-tested; needs checking on the ripping PC.
4. 🔶 **Ripping** — space check with reservations, rip with live progress, temp folder then rename (`Name/Name.mkv`, ` (2)` suffix if the folder exists), Ctrl-C stops MakeMKV and removes the partial rip; `blurip once`. Profile generation deferred (see Track selection). Built and tested with a fake MakeMKV; needs a real rip on the ripping PC.
5. ✅ **Jobs** — every outcome recorded (succeeded / failed / interrupted / skipped, with reason); disc fingerprint; skip discs with a previous successful rip (ask mode asks "rip again?"); single-instance lock; at startup, unfinished jobs are marked interrupted and leftover temp folders removed; `blurip history [-failed] [-n N]`.
6. ✅ **Ask-every-time mode** — shows every title (length, size, chapters, audio, subtitles); rip the main feature, choose titles (`1,3` or `0-2`), or skip; edit the folder name. Several titles rip into one folder, keeping MakeMKV's file names. *Saving choices as new defaults* is deferred until audio/subtitle selection exists (`--profile`), since a hand-picked title list is specific to one disc.
7. ✅ **Watch loop and multi-drive** — plain `blurip` watches and rips until Ctrl-C. Single/sequential: one disc at a time, no drive watched during a rip. Parallel: a worker per disc up to `max_concurrent`, idle drives keep being watched, shared space reservations, one live status line per drive, questions asked one drive at a time (other output held back meanwhile), Ctrl-C interrupts every rip. Questions now respond to Ctrl-C immediately. Built and tested with fake drives/MakeMKV; needs checking on the ripping PC.
8. **Polish** — `history`/`config` commands, release build.

## Screen

On a terminal, blurip draws a panel at the bottom, redrawn in place and cut to the terminal width:

```
blurip · watching sr0, sr1 · Ctrl-C to stop
sr0  waiting for a disc
sr1  Wild Card → /rips/Wild Card/Wild Card.mkv
     Saving to MKV file [██████░░░░░░░░░░░░░░]  31.0%  ETA 33m10s
```

Only lasting events scroll above it, one timestamped line each (finished, failed, skipped, warnings such as a failed eject). The panel is hidden while questions are asked. When output is not a terminal, plain log lines are written instead (progress every 10%).

## Known risks

- MakeMKV's default selection rule may leave out some audio tracks (e.g. a second track in the same language). Check the first real rip; if tracks are missing, set MakeMKV's "Default selection rule" to `+sel:all` or implement `--profile`.
- `--profile` needs a MakeMKV profile file as a template; `find` found none on the ripping PC.
- Blu-rays with playlist obfuscation can make "largest title" pick the wrong playlist; ask mode is the escape hatch.
