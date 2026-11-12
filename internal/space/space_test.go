package space

import (
	"errors"
	"strings"
	"testing"
)

const gib = 1 << 30

func TestCheck(t *testing.T) {
	u := Usage{Total: 1000 * gib, Free: 400 * gib} // 60% used
	cases := []struct {
		name              string
		needed, reserved  uint64
		pct               int
		wantErr, overFill bool
	}{
		{"fits", 100 * gib, 0, 80, false, false},
		{"exactly at limit", 200 * gib, 0, 80, false, false},
		{"over fill limit", 201 * gib, 0, 80, true, true},
		{"reservations push over limit", 100 * gib, 150 * gib, 80, true, true},
		{"not enough free space", 450 * gib, 0, 100, true, false},
		{"100% allows filling free space", 400 * gib, 0, 100, false, false},
	}
	for _, c := range cases {
		err := Check(u, c.needed, c.reserved, c.pct)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		var nr *NoRoomError
		if err != nil && (!errors.As(err, &nr) || nr.OverFillLimit != c.overFill) {
			t.Errorf("%s: err = %#v", c.name, err)
		}
	}
}

func TestNoRoomMessages(t *testing.T) {
	u := Usage{Total: 1000 * gib, Free: 400 * gib}
	err := Check(u, 300*gib, 0, 80)
	if err == nil || !strings.Contains(err.Error(), "fill the disk to 90% (limit 80%)") {
		t.Errorf("err = %v", err)
	}
	err = Check(u, 300*gib, 200*gib, 100)
	if err == nil || !strings.Contains(err.Error(), "200 GiB reserved") || !strings.Contains(err.Error(), "400 GiB free") {
		t.Errorf("err = %v", err)
	}
}

func TestStat(t *testing.T) {
	u, err := Stat(t.TempDir())
	if err != nil || u.Total == 0 || u.Free > u.Total {
		t.Errorf("Stat = %+v, %v", u, err)
	}
	if _, err := Stat("/nonexistent/path"); err == nil {
		t.Error("expected error")
	}
}

func TestReservations(t *testing.T) {
	dir := t.TempDir()
	u, _ := Stat(dir)
	var r Reservations
	// Reserve just under half of the free space twice; the second must fail.
	half := u.Free/2 + 1
	release, err := r.Reserve(dir, half, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reserve(dir, half, 100); err == nil {
		t.Fatal("second reservation should not fit")
	}
	release()
	release() // idempotent
	if r.reserved != 0 {
		t.Fatalf("reserved = %d after release", r.reserved)
	}
	if rel, err := r.Reserve(dir, half, 100); err != nil {
		t.Fatalf("after release: %v", err)
	} else {
		rel()
	}
}
