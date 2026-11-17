package awake

import (
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// errNoDesktop means there's no desktop session to ask (no session bus, or
// nothing on it that handles inhibit requests), so only logind's lock
// applies, which is all a PC without a desktop needs.
var errNoDesktop = errors.New("no desktop session")

// Inhibit flag shared by the XDG portal and GNOME's session manager:
// suspend, not idle, so the screen can still blank and lock.
const inhibitSuspend = 4

// desktopInhibit asks the desktop itself not to suspend. logind lets a
// user's own session override a block lock held by the same user, and
// KDE and GNOME suspend when idle without checking logind's locks, so
// they're asked through their own interfaces: the XDG desktop portal,
// or failing that GNOME's session manager or the freedesktop
// PowerManagement service (KDE). The request lasts as long as the D-Bus
// connection, so it goes away by itself if blurip dies.
func desktopInhibit(why string) (func(), <-chan error, error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, nil, errNoDesktop
	}
	if err := conn.Auth(nil); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("session bus: %w", err)
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("session bus: %w", err)
	}
	if err := askDesktop(conn, why); err != nil {
		conn.Close()
		return nil, nil, err
	}

	var mu sync.Mutex
	stopped := false
	lost := make(chan error, 1)
	go func() {
		<-conn.Context().Done()
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			lost <- errors.New("the connection to the desktop session closed")
		}
		close(lost)
	}()
	stop := func() {
		mu.Lock()
		stopped = true
		mu.Unlock()
		conn.Close()
	}
	return stop, lost, nil
}

// askDesktop makes the first inhibit request that something on the
// session bus answers.
func askDesktop(conn *dbus.Conn, why string) error {
	type try struct {
		name, path, method string
		args               []any
	}
	tries := []try{
		{"org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop", "org.freedesktop.portal.Inhibit.Inhibit",
			[]any{"", uint32(inhibitSuspend), map[string]dbus.Variant{"reason": dbus.MakeVariant(why)}}},
		{"org.gnome.SessionManager", "/org/gnome/SessionManager", "org.gnome.SessionManager.Inhibit",
			[]any{"blurip", uint32(0), why, uint32(inhibitSuspend)}},
		{"org.freedesktop.PowerManagement", "/org/freedesktop/PowerManagement/Inhibit", "org.freedesktop.PowerManagement.Inhibit.Inhibit",
			[]any{"blurip", why}},
	}
	var errs []error
	for _, t := range tries {
		call := conn.Object(t.name, dbus.ObjectPath(t.path)).Call(t.method, 0, t.args...)
		if call.Err == nil {
			return nil
		}
		var de dbus.Error
		if errors.As(call.Err, &de) && (de.Name == "org.freedesktop.DBus.Error.ServiceUnknown" ||
			de.Name == "org.freedesktop.DBus.Error.NameHasNoOwner") {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", t.name, call.Err))
	}
	if len(errs) == 0 {
		return errNoDesktop
	}
	return errors.Join(errs...)
}

// both takes logind's lock and the desktop's. It fails only if neither
// is taken. A desktop that's there but refuses is reported through lost,
// as the PC may then still sleep; logind refusing isn't, when the
// desktop's lock (the one that matters there) was taken.
func both(why string) (func(), <-chan error, error) {
	stopL, lostL, errL := systemdInhibit(why)
	stopD, lostD, errD := desktopInhibit(why)
	noDesktop := errors.Is(errD, errNoDesktop)
	if errL != nil && errD != nil {
		if noDesktop {
			return nil, nil, errL
		}
		return nil, nil, errors.Join(errL, errD)
	}
	lost := make(chan error, 2)
	var wg sync.WaitGroup
	forward := func(c <-chan error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for err := range c {
				lost <- err
			}
		}()
	}
	if errL == nil {
		forward(lostL)
	}
	if errD == nil {
		forward(lostD)
	} else if !noDesktop {
		lost <- fmt.Errorf("couldn't ask the desktop not to suspend: %w", errD)
	}
	go func() { wg.Wait(); close(lost) }()
	return func() {
		if stopL != nil {
			stopL()
		}
		if stopD != nil {
			stopD()
		}
	}, lost, nil
}
