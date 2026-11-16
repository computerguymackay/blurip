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
| DVD / Blu-ray folders | Optional (`split_by_type`, asked in setup): DVDs go to `<output>/DVD`, Blu-rays to `<output>/Blu-ray`; discs of unknown type stay in `<output>`. The temp folder stays at `<output>/.blurip-tmp`, so moving a finished rip is still an instant rename. |
| Drive nicknames | Each drive gets a nickname in setup ("Blu-ray drive", "DVD drive", or anything), shown as `Blu-ray drive (sr1)` in the panel, log lines, questions, eject warnings, `history` and `config`. Commands accept a nickname wherever they take a drive. Change with `blurip config drive sr1 name "Drive 1"`. |
| Reading a disc | Limited to 3 minutes; a disc that can't be read in time is recorded as failed and ejected, and the other drives carry on. |
| Missing output folder | Never created at rip time: a missing folder usually means its disk isn't mounted, and the rip would land on the wrong disk. |
| Partial rips | Rip into `<output>/.blurip-tmp/<job-id>/`, rename into place on success |

## Drive modes

| Mode | Behaviour |
|---|---|
| `single` (default) | One configured drive |
| `sequential` | Watch all enabled drives all the time; rip one disc at a time. A disc inserted while another rips is read straight away (questions asked in ask mode) and waits its turn ("Ready — rips next"), so it starts the moment the current rip ends. Each finished disc is ejected at once. |
| `parallel` | Same, but up to `max_concurrent` discs rip at once (0 = one per drive). |

Parallel-mode implications:

- Space check subtracts the estimated size of every in-flight rip ("reservations").
- Ask-every-time prompts are serialised: one prompt at a time; other rips keep going.
- Progress display: one updating line per active drive.
- All DB writes go through a single writer (WAL mode).
- Ctrl-C stops every in-flight rip, cleans each up and marks each `interrupted`.

## Startup

Plain `blurip` first lists the drives (nickname, model, disc status; also drives that are turned off, not connected, or connected but not set up) and a one-line summary of past rips (successful with total size, failed, interrupted, skipped). With two or more usable drives it asks how to use them this time: one drive only (then which one), all drives one rip at a time, or all at the same time. Enter keeps the last choice, and the answer is saved as the next default (`drive_mode`, `single_drive`). With one usable drive it just says which; when input isn't a terminal it uses the saved choice without asking.

## Box sets (`blurip -B` / `--box-set`)

For TV box sets every disc in the run is treated as part of a series:

- **Episodes**: titles under 10 minutes are extras; duplicates (same segment map, or same length and size) are dropped; a "play all" title (as long as the others together, within 2%) is left out. The episodes are the biggest group of titles within 15% of each other's length; a title about twice that length is kept as a double episode (flagged). Shorter titles are extras; much longer ones are left out and flagged. A "play all" covering only two episodes is flagged because it could be a double episode. Extras are never ripped.
- **Show, season, disc**: read from MakeMKV's disc name or the volume label (`FRIENDS_S1_D2`, `SOPRANOS_S01D03`, `THE_WIRE_SEASON_1_DISC_2`, `SERIES 2 DISK 1`, `KILLJOYS_S1D1_UPB1`, "Killjoys: Season One (Disc 1)", "The Complete Second Season", ...). Labels covering two seasons (`COLUMBO_SEASON6AND7_DISC1`, `SEASONS 1-2`) are numbered as the later season once that has been started, otherwise the first, and always checked; one disc can't be split between two seasons.
- **Saved progress** (migration 0005): `box_sets` (show, season, last disc, next episode) and `disc_episodes` (which episode each title of a disc became). Numbers are reserved when a disc is planned, not when its rip succeeds, so with two drives disc 2 can be numbered while disc 1 rips; a disc put back in after a failed or stopped rip (even after a restart) gets the same numbers without asking. Next episode and last disc only move forward; `blurip boxset set` corrects them.
- **Numbering**: episodes carry on from the last disc of the same show and season. blurip asks on the first disc of each show and season in a run, showing where a saved season had got to ("Carrying on Friends season 1 from S01E07 (last ripped: disc 2, 12 Nov 21:40)"); also when the disc number isn't the last + 1, a title is flagged, or no episodes are found (rip as shown / change show, season, first episode, titles / skip). Later discs in the run go straight through.
- **Several box sets at once**: progress is per show and season, so two box sets in two drives each keep their own numbering. A label without a show name is taken to be the show being ripped if there is only one; with several it asks, and naming the show and season picks up that season's next episode.
- **Files**: `<output>[/DVD|/Blu-ray]/Show/Season 01/Show - S01E01.mkv`. Each disc adds to the season folder; an existing file is never overwritten (` (2)` is added).
- **Several drives**: one numbering is shared by all drives. Discs are read at the same time, so a disc that is ahead of its turn (disc 2 read while another drive is still reading) waits until the other disc has been numbered; episodes follow the disc numbers, not which drive finished reading first. Once numbered, discs rip in sequential or parallel mode as usual. A first disc that isn't disc 1 (part way through a season) is flagged with a hint to enter the first episode.
- Needs a terminal (the first disc is always confirmed). Every disc is still a normal job (status, times, bytes); the job's output path is the season folder. `blurip history` shows each disc's episodes ("Friends S01E04–S01E06"), `blurip history JOB` which title became which episode.
- `blurip boxset` lists box sets (discs, episodes, last disc, next episode); `boxset set SHOW SEASON next N | disc N` corrects one; `boxset forget SHOW SEASON` drops it (files and job history stay).

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
| `blurip setup` | Once set up: list every setting with its value and change single ones (a = go through them all, Enter saves, q quits without saving). The first run goes through everything. `blurip encode setup` does the same for the encoding settings. |
| `blurip config [get KEY \| set KEY VALUE \| drive DEV on\|off]` | View or change settings; turn configured drives on/off |
| `blurip version` | Version, set at build time |
| `blurip history [-failed] [-n N]` / `blurip history JOB` | Job history (with drive mode); everything about one job, including where the time went |
| `blurip once` | Rip the current disc and exit |
| `blurip drive status\|watch\|eject` | Diagnostic: drive status, watch for discs, eject and confirm |
| `blurip probe` | Diagnostic: scan drives, print parsed disc info and the chosen main feature, optionally save raw `makemkvcon` output |

## Schema (initial)

- `settings(key, value)`
- `drives(id, device, name, nickname, enabled)`
- `discs(id, fingerprint, name, volume_label, disc_type, first_seen)`
- `jobs(id, disc_id, drive_id, started_at, finished_at, status, failure_reason, output_path, bytes_written,
  drive_mode, drives_used, rips_at_once, read_seconds, wait_seconds, rip_seconds)`
  — status: `running | succeeded | failed | interrupted | skipped`
  — `started_at` is when the disc was picked up; total time = `finished_at − started_at` = reading + waiting for its turn + ripping
  — `drive_mode`: `single | sequential | parallel` (the mode in effect; one enabled drive is always `single`)
  — `drives_used`: the drives blurip was using, as labels (a snapshot, so later renames don't rewrite history)
  — `rips_at_once`: the most rips running at the same time during this one (1 = alone, 0 = never ripped)
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
8. ✅ **Polish** — `config get/set/drive` (validated; paths checked; notes when a running blurip needs restarting), separate DVD/Blu-ray folders, disc type in `history`, refuses to rip into a missing output folder, `blurip version`, `Makefile` (`build`, `test`, `deploy` to the ripping PC, `release` for amd64/arm64 with checksums).
9. 🔶 **Multi-drive flow** — drive nicknames, time limit on reading a disc, one dispatcher for sequential and parallel mode that reads new discs during other rips and queues them. Tested with fake drives/MakeMKV; needs two real drives (and running `makemkvcon info` during a rip) checked on the ripping PC.
10. 🔶 **Startup screen** — drive list, rip statistics, choose the drive mode (and single drive) each run; `-s`/`--skip` uses last time's choice without asking. Tested with fake drives; needs a look on the ripping PC.
11. 🔶 **Box sets** (branch `box-set`) — `-B`/`--box-set`: episode detection, S01E01 naming from the disc label, numbering across discs and drives saved in the database, confirm on the first disc of each season per run, warn and ask on surprises, episodes in `history`, `blurip boxset`. Tested with a fake MakeMKV; needs real box-set discs (`blurip probe` output) to tune the detection.
12. 🔶 **HandBrake encoding** (branch `handbrake`) — see below: `-Q` queueing and the foreign-film question, `blurip encode`, `encode -U`, preset menus, deleting rips after a checked encode. Built and tested with a fake HandBrakeCLI (including through a pseudo-terminal: `-U`, `q`, Ctrl-C); needs a real encode on the ripping PC.

## HandBrake encoding (branch `handbrake`)

Rips are big (a Blu-ray is ~30 GB). After ripping, each one is encoded with
HandBrake into a small file (1–2 GB) for the library, and the rip is deleted.

| Topic | Decision |
|---|---|
| Queueing | `blurip -Q` (`--queue`), with or without `-B`: every successful rip is added to an encode queue (one entry per title, so one per episode in box-set mode). Rips without `-Q` aren't queued; `encode -U` can add them. |
| Foreign films | In `-Q` mode blurip asks "Is Ip Man a foreign film? [y/N]" while the disc rips, the same way as the disc-name question, so it never holds up a rip. No answer = standard. In box-set mode it is asked once per box set (with the first disc's confirmation) and remembered. |
| Tracks | **Standard**: the preset decides (English audio + forced subtitles), output as the preset's format (MP4). **Foreign**: the same preset plus `--audio-lang-list any --all-audio --subtitle-lang-list any --all-subtitles --subtitle-burned=none --format av_mkv` for Blu-rays (and discs of unknown type). MKV because MP4 can't hold Blu-ray (PGS) subtitles except burned in; the size is the same as MP4 apart from the extra tracks. Foreign DVDs keep the preset's format (MP4), since MP4 can hold DVD (VobSub) subtitles as switchable tracks. |
| Presets | Chosen by each user in `blurip encode setup` (nothing is fixed: blurip is public); `General/Fast 1080p30` is suggested for both. On the ripping PC: DVD → `General/Fast 1080p30`, Blu-ray → `My Presets/720pFast` (the user's own, from the HandBrake app). Passed by name with `--preset-import-gui`, so editing a preset in the app takes effect. The output format comes from the preset (the app's presets file for the user's own presets, otherwise the description) and is always passed with `--format`, so the file's extension matches. |
| Choosing a preset | Two numbered menus, never typed: category (taken from `HandBrakeCLI --preset-import-gui --preset-list`: General, Web, Devices, Matroska, Hardware, Professional and the user's own "My Presets"; "CLI Defaults" is left out), then preset. Used in setup and `encode -U`; Enter keeps the current one. |
| HandBrake | The Flatpak: `flatpak run --command=HandBrakeCLI fr.handbrake.ghb` (native `HandBrakeCLI` if on the PATH). Found like makemkvcon. The Flatpak gets access to the rips folder and the destination drive, using the same check and offer to fix as MakeMKV. |
| Destinations | Two settings, both on a local drive: films → `movies` (`movies/Ip Man (2008)/Ip Man (2008).mkv`), box sets → `shows` (`shows/Killjoys/Season 01/Killjoys - S01E01.mp4`). Stand-up specials go to `movies` and are moved by hand (or pointed at another folder with `-U`). Copying to the server is done by hand; remote copying is not planned for now. A missing destination is never created (drive not mounted). |
| Film year | Discs rarely carry the year, so it comes from the name: name the disc "Ip Man (2008)" (ask mode, or the code-name question) and the encode is `Ip Man (2008)/Ip Man (2008).mp4`. Online lookup (TMDb) not planned for now. |
| Encoding | Into `<destination>/.blurip-tmp/`, then renamed into place, so a half-written file never appears in the library. Checked afterwards: HandBrake succeeded, the file exists and its length matches the rip's. |
| Original rip | Deleted (setting, default yes) only after its encode has been checked. A box-set disc's folder goes when all its episodes are done. A failed encode keeps the rip and records the reason. |
| Stopping | `q` finishes the current encode then stops ("stopping after this one"; `q` again cancels). Ctrl-C stops at once; that entry goes back to waiting and starts from the beginning next time (HandBrake can't resume). An entry left "encoding" by a crash is reset to waiting at startup. |
| Alongside ripping | `blurip encode` can run in a second terminal while `blurip -Q` rips; it waits for new entries instead of exiting when the queue is empty. Both use the same database. |

### Commands

- `blurip encode` — encode the queue until it is empty (or keep waiting with `-w`); `q` / Ctrl-C to stop.
- `blurip encode -U` (`--update-list`) — edit the queue, then "Start encoding now? [Y/n]":

```
Encode queue: 4 waiting
  #  Title                     From     Preset          Tracks     To
  1  Wild Card                 Blu-ray  720pFast        Standard   movies
  2  Ip Man (2008)             Blu-ray  720pFast        Foreign    movies
  3  Killjoys - S01E01         DVD      Fast 1080p30    Standard   shows
  4  Killjoys - S01E02         DVD      Fast 1080p30    Standard   shows

Number to change (3-4 for several), a to add, q to finish:
```

  Per entry: tracks (standard / foreign), preset (category and preset menus), destination (movies, shows or a folder), remove. Add offers past rips that aren't queued, or any MKV file by path. Removed entries stay in history.
- `blurip encode list [-a]` — the queue (with `-a`, finished and removed entries too), with failure reasons and where finished encodes went.
- `blurip encode setup` — HandBrakeCLI, the two presets, the folders, deleting rips. Also run by the first `blurip -Q` or `blurip encode`.

### Schema

- Migration `0007_encode_queue.sql`: `encodes(id, job_id, source_path, disc_kind, title, preset, all_tracks,
  tracks_answered, dest_dir, dest_name, status waiting|encoding|done|failed|removed, failure_reason, output_path,
  queued_at, started_at, finished_at, bytes_in, bytes_out, source_deleted)`.
  Each entry stores its own settings, so changing the defaults never changes entries already queued.
- `show_tracks(show, all_tracks)`: the box-set answer, per show.
- Settings: `handbrake_path`, `preset_dvd`, `preset_bluray`, `movies_dir`, `shows_dir`, `delete_after_encode`.
- Only one `blurip encode` runs at a time (lock file `<db>.encode.lock`); it can run alongside the ripper.

### Checked on the ripping PC

- The Flatpak includes HandBrakeCLI, and `--preset-import-gui --preset-list` lists the user's own presets
  (`My Presets/720pFast`) after the built-in ones (`internal/handbrake/testdata/preset-list.txt`).
- No hardware encoders are available there (NVENC, VCN, QSV), so encodes run on the CPU.

### Still to check on the ripping PC

- A real encode: HandBrakeCLI's progress line (`Encoding: task 1 of 1, 45.32 % ...`) drives the progress bar, and
  its scan (`+ duration: 01:47:03`) the length check.
- The foreign-film options on a real Blu-ray (`--subtitle-burned=none`, all tracks into MKV) and a real DVD
  (all tracks, DVD subtitles kept in MP4).
- The Flatpak's access to the rips and destination folders (`/run/media/...`).

## Screen

On a terminal, blurip draws a panel at the bottom, redrawn in place and cut to the terminal width:

```
blurip · watching DVD drive (sr0), Blu-ray drive (sr1) (one rip at a time) · Ctrl-C to stop
DVD drive (sr0)      The Predator
                     Ready — rips next
Blu-ray drive (sr1)  Wild Card → /rips/Blu-ray/Wild Card/Wild Card.mkv
                     Saving to MKV file [██████░░░░░░░░░░░░░░]  31.0%  ETA 33m10s
```

Only lasting events scroll above it, one timestamped line each (finished, failed, skipped, warnings such as a failed eject). While questions are asked the live panel is replaced by a still copy of every drive's entry (disc, activity, progress at that moment), so you can tell which disc is in which drive while answering; it goes live again afterwards. When output is not a terminal, plain log lines are written instead (progress every 10%).

## Known risks

- MakeMKV's default selection rule may leave out some audio tracks (e.g. a second track in the same language). Check the first real rip; if tracks are missing, set MakeMKV's "Default selection rule" to `+sel:all` or implement `--profile`.
- `--profile` needs a MakeMKV profile file as a template; `find` found none on the ripping PC.
- Blu-rays with playlist obfuscation can make "largest title" pick the wrong playlist; ask mode is the escape hatch.
