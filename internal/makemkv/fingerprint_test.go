package makemkv

import (
	"strings"
	"testing"
)

func parseString(t *testing.T, s string) *Info {
	t.Helper()
	info, err := ParseInfo(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestFingerprint(t *testing.T) {
	base := parseFile(t, "bluray-synthetic.txt")
	fp := Fingerprint(base)
	if len(fp) != 32 {
		t.Errorf("fingerprint %q", fp)
	}
	if Fingerprint(parseFile(t, "bluray-synthetic.txt")) != fp {
		t.Error("fingerprint not stable")
	}

	// Same disc in another drive: DRV lines and messages differ, fingerprint must not.
	raw := `DRV:0,2,999,12,"Other drive","EXAMPLE_MOVIE","/dev/sr0"
MSG:9999,0,0,"something","something"
CINFO:1,6209,"Blu-ray disc"
CINFO:32,0,"EXAMPLE_MOVIE"
TINFO:0,9,0,"1:47:12"
TINFO:0,11,0,"34467127296"
`
	a := Fingerprint(parseString(t, raw))
	b := Fingerprint(parseString(t, strings.Replace(raw, "Other drive", "Third drive", 1)))
	if a != b {
		t.Error("fingerprint depends on the drive")
	}
	// Disc 2 of a set: same label, different titles.
	c := Fingerprint(parseString(t, strings.Replace(raw, "1:47:12", "1:12:03", 1)))
	if a == c {
		t.Error("different titles gave the same fingerprint")
	}
}
