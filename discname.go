package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/computerguymackay/blurip/internal/rip"
)

// discNamer asks for a disc's name when the name it came with looks like a
// code ("SBK0X7AJ"). The question doesn't hold anything up: the disc rips
// under the code name, and the answer is used when the rip is moved out of
// the temp folder, or, if the rip has already been moved, renames it.
type discNamer struct {
	s      *session
	discID int64
	code   string // the disc's own name

	mu       sync.Mutex
	answer   string      // the name given; "" until answered
	destDir  string      // where the rip goes; "" until it starts
	finished bool        // the rip has ended (any outcome)
	jobID    int64       // set when the rip succeeded
	placed   *rip.Result // the rip in its final place, if it succeeded
}

// question asks for the name. It is asked in the background while the
// disc rips (see session.askLater), with the questions lock held.
func (n *discNamer) question(ctx context.Context) {
	s := n.s
	fmt.Fprintf(s.out, "\nThe disc in %s is called %q, which looks like a code rather than a title.\n", s.view.label, n.code)
	fmt.Fprintln(s.out, "It is being ripped under that name; give it a proper one and the rip will be renamed.")
	ans, err := s.ask.String("Name for this disc", n.code)
	if err != nil {
		return // Ctrl-C or no more input: keep the code name
	}
	if name := rip.SafeName(ans); name != "" && name != n.code {
		n.setAnswer(context.WithoutCancel(ctx), name)
	}
}

func (n *discNamer) setAnswer(dbctx context.Context, name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.answer = name
	if err := n.s.store.SetDiscName(dbctx, n.discID, name); err != nil {
		n.s.view.Log("⚠ couldn't save the name %q: %v", name, err)
	}
	switch {
	case n.placed != nil:
		n.renameLocked(dbctx)
	case !n.finished:
		target := ""
		if n.destDir != "" {
			target = filepath.Join(n.destDir, name, name+".mkv")
		}
		n.s.view.SetDisc(name, target)
	}
}

// current is the name to use now: the answer if there is one yet.
func (n *discNamer) current() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.answer != "" {
		return n.answer
	}
	return n.code
}

// start records that the rip is starting into destDir and returns the
// name to show.
func (n *discNamer) start(destDir string) string {
	n.mu.Lock()
	n.destDir = destDir
	n.mu.Unlock()
	return n.current()
}

// finish records how the rip ended. If it succeeded under a name other
// than the answer (the answer came after the move), the rip is renamed.
func (n *discNamer) finish(dbctx context.Context, jobID int64, res *rip.Result) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.finished = true
	if res == nil {
		return
	}
	n.jobID, n.placed = jobID, res
	if n.answer != "" {
		n.renameLocked(dbctx)
	}
}

func (n *discNamer) renameLocked(dbctx context.Context) {
	old := *n.placed
	if old.Name == n.answer {
		return
	}
	res, err := rip.Rename(old, old.Name, n.answer)
	if err != nil {
		n.s.view.Log("⚠ couldn't rename %s to %q: %v", old.Dir, n.answer, err)
		return
	}
	n.placed = &res
	if err := n.s.store.SetJobOutputPath(dbctx, n.jobID, res.Dir); err != nil {
		n.s.view.Log("⚠ couldn't record the new folder %s: %v", res.Dir, err)
	}
	n.s.renameQueued(dbctx, n.jobID, old, res)
	n.s.view.Log("✎ %s renamed to %s → %s", old.Name, res.Name, res.Dir)
}
