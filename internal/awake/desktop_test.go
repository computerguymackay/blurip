package awake

import (
	"bufio"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// sessionBus starts a private D-Bus session bus for the test and points
// DBUS_SESSION_BUS_ADDRESS at it.
func sessionBus(t *testing.T) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon isn't installed")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))
	return cmd
}

// fakePortal answers org.freedesktop.portal.Inhibit.Inhibit on the bus
// and reports each request's sender, flags and reason.
type fakePortal struct{ got chan [3]string }

func (p *fakePortal) Inhibit(sender dbus.Sender, window string, flags uint32, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	reason, _ := opts["reason"].Value().(string)
	p.got <- [3]string{string(sender), string(rune('0' + flags)), reason}
	return "/org/freedesktop/portal/desktop/request/1/x", nil
}

func TestDesktopInhibitPortal(t *testing.T) {
	sessionBus(t)
	srv, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	p := &fakePortal{got: make(chan [3]string, 1)}
	if err := srv.Export(p, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.Inhibit"); err != nil {
		t.Fatal(err)
	}
	if r, err := srv.RequestName("org.freedesktop.portal.Desktop", 0); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName: %v %v", r, err)
	}
	// Watch for the client's name going away, as the real portal does.
	if err := srv.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged")); err != nil {
		t.Fatal(err)
	}
	sig := make(chan *dbus.Signal, 10)
	srv.Signal(sig)

	stop, lost, err := desktopInhibit("Encoding with HandBrake")
	if err != nil {
		t.Fatal(err)
	}
	got := <-p.got
	if got[1] != "4" || got[2] != "Encoding with HandBrake" {
		t.Errorf("flags, reason = %q, %q", got[1], got[2])
	}
	stop()
	for {
		select {
		case s := <-sig:
			if len(s.Body) == 3 && s.Body[0] == got[0] && s.Body[2] == "" {
				if err, ok := <-lost; ok {
					t.Errorf("stopped request reported as lost: %v", err)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the connection holding the request wasn't closed")
		}
	}
}

func TestDesktopInhibitNoDesktop(t *testing.T) {
	sessionBus(t) // a bus with nothing on it
	if _, _, err := desktopInhibit("x"); !errors.Is(err, errNoDesktop) {
		t.Errorf("err = %v, want errNoDesktop", err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	if _, _, err := desktopInhibit("x"); !errors.Is(err, errNoDesktop) {
		t.Errorf("err = %v, want errNoDesktop", err)
	}
}

func TestDesktopInhibitLost(t *testing.T) {
	bus := sessionBus(t)
	srv, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	p := &fakePortal{got: make(chan [3]string, 1)}
	srv.Export(p, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.Inhibit")
	srv.RequestName("org.freedesktop.portal.Desktop", 0)
	stop, lost, err := desktopInhibit("x")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	<-p.got
	// The session bus going away (logging out) loses the request.
	bus.Process.Kill()
	select {
	case err := <-lost:
		if err == nil {
			t.Error("lost closed without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lost request not reported")
	}
}
