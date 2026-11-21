package handbrake

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

// Request is one encode.
type Request struct {
	Bin    string
	Input  string
	Output string
	Preset string // a preset name ("Fast 1080p30") or "Category/Name"
	Format string // "mp4", "mkv" or "webm"; always given, so it matches Output's extension
	// AllTracks keeps every audio and subtitle track (foreign films). The
	// subtitles are kept as tracks you can switch on, never burned in, so
	// this needs Format "mkv" for Blu-ray (PGS) subtitles; DVD (VobSub)
	// subtitles fit in MP4.
	AllTracks bool
	// Chapters is the chapter range to encode ("6-10"); "" = all.
	Chapters   string
	OnProgress func(Progress)
	StopGrace  time.Duration // after SIGTERM, before SIGKILL; 0 = 10s
}

// Progress is HandBrake's progress through an encode.
type Progress struct {
	Task     string  // "Encoding", "Scanning", "Muxing"
	Fraction float64 // 0 to 1, over all of HandBrake's passes
}

// Args returns HandBrakeCLI's arguments for r.
func (r Request) Args() []string {
	_, name := SplitRef(r.Preset)
	args := []string{"-i", r.Input, "-o", r.Output, "--preset-import-gui", "--preset", name, "--format", "av_" + r.Format}
	if r.Chapters != "" {
		args = append(args, "--chapters", r.Chapters)
	}
	if r.AllTracks {
		args = append(args,
			"--audio-lang-list", "any", "--all-audio",
			"--subtitle-lang-list", "any", "--all-subtitles",
			"--subtitle-burned=none")
	}
	return args
}

// EncodeError is a failed encode with HandBrake's last log lines.
type EncodeError struct {
	Err error
	Log string
}

func (e *EncodeError) Error() string {
	if e.Log == "" {
		return fmt.Sprintf("HandBrakeCLI failed: %v", e.Err)
	}
	return fmt.Sprintf("HandBrakeCLI failed: %v: %s", e.Err, e.Log)
}

func (e *EncodeError) Unwrap() error { return e.Err }

// Encode runs HandBrakeCLI. Cancelling ctx stops it (SIGTERM to its
// process group, then SIGKILL) and returns ctx.Err(); the part-written
// output is left for the caller to remove.
func Encode(ctx context.Context, r Request) error {
	if r.StopGrace == 0 {
		r.StopGrace = 10 * time.Second
	}
	a := makemkv.Argv(r.Bin)
	cmd := execCommand(a[0], append(a[1:], r.Args()...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log := &tail{max: 64 << 10}
	cmd.Stderr = log
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting HandBrakeCLI: %w", makemkv.ExplainStartError(err))
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(r.StopGrace):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()

	readProgress(stdout, r.OnProgress)
	waitErr := cmd.Wait()
	close(done)
	<-stopped

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return &EncodeError{Err: waitErr, Log: lastLines(log.Bytes(), 3)}
	}
	return nil
}

// progressRE matches HandBrakeCLI's progress line:
//
//	Encoding: task 1 of 1, 45.32 % (120.51 fps, avg 110.22 fps, ETA 00h12m30s)
var progressRE = regexp.MustCompile(`^(\w[\w ]*?):? task (\d+) of (\d+), (\d+(?:\.\d+)?) %`)

// ParseProgress reads one progress line.
func ParseProgress(line string) (Progress, bool) {
	m := progressRE.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Progress{}, false
	}
	task, _ := strconv.Atoi(m[2])
	tasks, _ := strconv.Atoi(m[3])
	pct, _ := strconv.ParseFloat(m[4], 64)
	if tasks < 1 || task < 1 {
		return Progress{}, false
	}
	f := (float64(task-1) + pct/100) / float64(tasks)
	return Progress{Task: m[1], Fraction: min(max(f, 0), 1)}, true
}

// readProgress reads HandBrakeCLI's stdout, where progress lines end in
// \r rather than \n, until it closes.
func readProgress(r io.Reader, on func(Progress)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	sc.Split(splitCRLF)
	for sc.Scan() {
		if p, ok := ParseProgress(sc.Text()); ok && on != nil {
			on(p)
		}
	}
	_, _ = io.Copy(io.Discard, r) // never leave HandBrake blocked on a full pipe
}

func splitCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// durationRE matches the title duration in a scan: "  + duration: 01:47:03".
var durationRE = regexp.MustCompile(`\+ duration: (\d+):(\d\d):(\d\d)`)

// chapterRE matches a chapter in a scan: "    + 3: duration 00:04:52"
// (allowing for a chapter name before "duration").
var chapterRE = regexp.MustCompile(`(?m)^\s*\+ (\d+): (?:.*?)duration (\d+):(\d\d):(\d\d)`)

// ScanResult is what HandBrake's scan says about a video file.
type ScanResult struct {
	Duration time.Duration
	Chapters []time.Duration // each chapter's length, in order
}

// Scan scans a video file with HandBrake, so blurip needs no other tools.
func Scan(ctx context.Context, bin, file string) (ScanResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := commandContext(ctx, bin, "--scan", "-i", file).CombinedOutput()
	if ctx.Err() != nil {
		return ScanResult{}, ctx.Err()
	}
	return parseScan(out, file, err)
}

func parseScan(out []byte, file string, runErr error) (ScanResult, error) {
	m := durationRE.FindSubmatch(out)
	if m == nil {
		if runErr != nil {
			return ScanResult{}, fmt.Errorf("scanning %s: %w: %s", file, runErr, lastLines(out, 2))
		}
		return ScanResult{}, fmt.Errorf("scanning %s: HandBrake found no video in it", file)
	}
	r := ScanResult{Duration: hms(m[1], m[2], m[3])}
	for _, c := range chapterRE.FindAllSubmatch(out, -1) {
		if n, _ := strconv.Atoi(string(c[1])); n != len(r.Chapters)+1 {
			break // a second title's chapters
		}
		r.Chapters = append(r.Chapters, hms(c[2], c[3], c[4]))
	}
	return r, nil
}

func hms(h, m, s []byte) time.Duration {
	hi, _ := strconv.Atoi(string(h))
	mi, _ := strconv.Atoi(string(m))
	si, _ := strconv.Atoi(string(s))
	return time.Duration(hi)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(si)*time.Second
}

// Duration returns the running time of a video file.
func Duration(ctx context.Context, bin, file string) (time.Duration, error) {
	r, err := Scan(ctx, bin, file)
	return r.Duration, err
}

// ChapterLength is how long chapters first to last are, from a scan's
// chapter lengths.
func ChapterLength(chapters []time.Duration, first, last int) (time.Duration, error) {
	if first < 1 || last < first || last > len(chapters) {
		return 0, fmt.Errorf("the rip has %d chapters, so it has no chapters %d-%d", len(chapters), first, last)
	}
	var d time.Duration
	for _, c := range chapters[first-1 : last] {
		d += c
	}
	return d, nil
}

// ErrLengthMismatch means an encode is not as long as its source.
var ErrLengthMismatch = errors.New("the encoded file is not as long as the rip")

// CheckLength compares an encode's length with its source's, allowing a
// few seconds' difference.
func CheckLength(source, encoded time.Duration) error {
	diff := source - encoded
	if diff < 0 {
		diff = -diff
	}
	if diff > max(5*time.Second, source/100) {
		return fmt.Errorf("%w: it is %s long, the rip %s", ErrLengthMismatch, encoded, source)
	}
	return nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tail) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}
