package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

// A folder on a network mount (sshfs, NFS, SMB) can go away while
// blurip encode --wait sits idle, typically when the PC sleeps: the
// mount drops, leaving an empty local folder in its place (encodes
// would then fill the local disk), or hangs, so anything touching it
// blocks. The encoder checks its folders (rips, films, TV) before every
// encode and every minute while waiting, and stops if one is gone,
// rather than encode to the wrong disk or hang.

// dirCheckEvery is how often the folders are checked while waiting.
var dirCheckEvery = time.Minute

// dirCheckTimeout is how long a folder may take to answer.
var dirCheckTimeout = 15 * time.Second

// errDirGone is the result of a check that found a folder inaccessible.
var errDirGone = errors.New("folder gone")

// fuseMagic is FUSE's filesystem type (sshfs, rclone mounts).
const fuseMagic = 0x65735546

// dirWatch remembers which filesystem each folder was on when encoding
// started, to notice one that has been unmounted.
type dirWatch struct {
	dirs   []string
	fsType map[string]int64
	statfs func(dir string) (int64, error)

	pending map[string]chan fsAnswer // checks still waiting for an answer
	last    time.Time                // when the folders were last checked
}

type fsAnswer struct {
	fsType int64
	err    error
}

func newDirWatch(dirs ...string) *dirWatch {
	d := &dirWatch{fsType: map[string]int64{}, pending: map[string]chan fsAnswer{}, statfs: statfsType}
	for _, dir := range dirs {
		if dir != "" && !slices.Contains(d.dirs, dir) {
			d.dirs = append(d.dirs, dir)
		}
	}
	return d
}

// statfsType is the type of the filesystem dir is on; it fails if dir
// isn't a folder that can be read.
func statfsType(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return 0, err
	} else if !fi.IsDir() {
		return 0, errors.New("not a folder")
	}
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	return int64(st.Type), nil
}

// check looks at every folder, each given dirCheckTimeout to answer, and
// returns what is wrong, one line per folder (nil: all fine). The first
// check records each folder's filesystem; later ones report a folder
// now on another filesystem: unmounted, leaving the empty folder it was
// mounted on.
func (d *dirWatch) check() []string {
	d.last = time.Now()
	var problems []string
	for _, dir := range d.dirs {
		ch, ok := d.pending[dir]
		if !ok {
			ch = make(chan fsAnswer, 1)
			d.pending[dir] = ch
			go func() {
				t, err := d.statfs(dir)
				ch <- fsAnswer{t, err}
			}()
		}
		var a fsAnswer
		select {
		case a = <-ch:
			delete(d.pending, dir)
		case <-time.After(dirCheckTimeout):
			// Still hanging: looked at again next time, without starting
			// another check that would hang too.
			problems = append(problems, fmt.Sprintf("%s isn't answering (a network mount that has dropped?)", dir))
			continue
		}
		was, seen := d.fsType[dir]
		switch {
		case a.err != nil:
			problems = append(problems, fmt.Sprintf("%s can't be read: %v", dir, explainFSError(a.err)))
		case !seen:
			d.fsType[dir] = a.fsType
		case a.fsType != was:
			what := "it is on another filesystem than when encoding started"
			if was == fuseMagic {
				what = "it was an sshfs (FUSE) mount and isn't any more"
			}
			problems = append(problems, fmt.Sprintf("%s looks unmounted: %s", dir, what))
		}
	}
	return problems
}

// explainFSError says what a dropped mount's error means.
func explainFSError(err error) string {
	if errors.Is(err, unix.ENOTCONN) {
		return "the mount has lost its connection (remount it)"
	}
	return err.Error()
}
