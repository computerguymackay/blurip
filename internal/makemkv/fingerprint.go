package makemkv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Fingerprint identifies a disc from its MakeMKV info: disc type, volume
// label, and each title's source file, duration, size and chapter count.
// The same disc gives the same fingerprint in any drive; different discs
// with the same label (e.g. disc 1 and disc 2 of a set) almost never do.
func Fingerprint(info *Info) string {
	var b strings.Builder
	fmt.Fprintf(&b, "v1\x00%s\x00%s\n", info.Disc.Type(), info.Disc.VolumeName())
	for _, t := range info.Titles { // ParseInfo sorts titles by ID
		fmt.Fprintf(&b, "%d\x00%s\x00%s\x00%s\x00%s\n", t.ID,
			t.SourceFile(), t.Attrs[AttrDuration], t.Attrs[AttrDiskSizeBytes], t.Attrs[AttrChapterCount])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}
