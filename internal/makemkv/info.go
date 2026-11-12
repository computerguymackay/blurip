package makemkv

import (
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Attribute IDs used in CINFO/TINFO/SINFO lines (from MakeMKV's apdefs.h).
const (
	AttrType             = 1
	AttrName             = 2
	AttrLangCode         = 3
	AttrLangName         = 4
	AttrCodecID          = 5
	AttrCodecShort       = 6
	AttrCodecLong        = 7
	AttrChapterCount     = 8
	AttrDuration         = 9
	AttrDiskSize         = 10
	AttrDiskSizeBytes    = 11
	AttrBitrate          = 13
	AttrAudioChannels    = 14
	AttrSourceFileName   = 16
	AttrVideoSize        = 19
	AttrStreamFlags      = 22
	AttrSegmentsCount    = 25
	AttrSegmentsMap      = 26
	AttrOutputFileName   = 27
	AttrMetadataLangCode = 28
	AttrTreeInfo         = 30
	AttrPanelTitle       = 31
	AttrVolumeName       = 32
	AttrOrderWeight      = 33
)

// DriveState is the second field of a DRV line.
type DriveState int

const (
	DriveEmptyClosed DriveState = 0
	DriveEmptyOpen   DriveState = 1
	DriveInserted    DriveState = 2
	DriveLoading     DriveState = 3
	DriveNoDrive     DriveState = 256
	DriveUnmounting  DriveState = 257
)

func (s DriveState) String() string {
	switch s {
	case DriveEmptyClosed:
		return "empty"
	case DriveEmptyOpen:
		return "open"
	case DriveInserted:
		return "disc inserted"
	case DriveLoading:
		return "loading"
	case DriveNoDrive:
		return "no drive"
	case DriveUnmounting:
		return "unmounting"
	}
	return "state " + strconv.Itoa(int(s))
}

// DiscFlags is the fourth field of a DRV line: which file systems are present.
type DiscFlags int

const (
	FlagDVDFiles    DiscFlags = 1
	FlagHDDVDFiles  DiscFlags = 2
	FlagBlurayFiles DiscFlags = 4
	FlagAACSFiles   DiscFlags = 8
	FlagBDSVMFiles  DiscFlags = 16
)

// Drive is a DRV line.
type Drive struct {
	Index     int
	State     DriveState
	Flags     DiscFlags
	DriveName string
	DiscLabel string
	Device    string
}

// Exists reports whether this DRV slot is a real drive.
func (d Drive) Exists() bool { return d.State != DriveNoDrive && d.Device != "" }

// HasDisc reports whether a disc is inserted and readable.
func (d Drive) HasDisc() bool { return d.State == DriveInserted }

// DiscKind returns "Blu-ray", "DVD" or "" based on the file-system flags.
func (d Drive) DiscKind() string {
	switch {
	case d.Flags&FlagBlurayFiles != 0:
		return "Blu-ray"
	case d.Flags&FlagDVDFiles != 0:
		return "DVD"
	}
	return ""
}

// Message is a MSG line.
type Message struct {
	Code   int
	Flags  int
	Text   string
	Format string
	Params []string
}

// Attrs holds attribute ID → value pairs.
type Attrs map[int]string

func (a Attrs) Int(id int) int {
	n, _ := strconv.Atoi(a[id])
	return n
}

// Disc is the set of CINFO attributes.
type Disc struct{ Attrs Attrs }

func (d Disc) Type() string       { return d.Attrs[AttrType] }
func (d Disc) Name() string       { return d.Attrs[AttrName] }
func (d Disc) VolumeName() string { return d.Attrs[AttrVolumeName] }

// Stream is one SINFO stream of a title.
type Stream struct {
	ID    int
	Attrs Attrs
}

func (s Stream) Type() string     { return s.Attrs[AttrType] } // "Video", "Audio", "Subtitles"
func (s Stream) LangCode() string { return s.Attrs[AttrLangCode] }
func (s Stream) LangName() string { return s.Attrs[AttrLangName] }
func (s Stream) Codec() string    { return s.Attrs[AttrCodecShort] }
func (s Stream) Channels() int    { return s.Attrs.Int(AttrAudioChannels) }

// Title is one TINFO title with its streams.
type Title struct {
	ID      int
	Attrs   Attrs
	Streams []Stream
}

func (t Title) Name() string       { return t.Attrs[AttrName] }
func (t Title) SourceFile() string { return t.Attrs[AttrSourceFileName] }
func (t Title) OutputFile() string { return t.Attrs[AttrOutputFileName] }
func (t Title) Chapters() int      { return t.Attrs.Int(AttrChapterCount) }
func (t Title) SizeHuman() string  { return t.Attrs[AttrDiskSize] }

// SizeBytes returns the estimated output size in bytes.
func (t Title) SizeBytes() int64 {
	n, _ := strconv.ParseInt(t.Attrs[AttrDiskSizeBytes], 10, 64)
	return n
}

// Duration parses the "h:mm:ss" duration attribute.
func (t Title) Duration() time.Duration { return parseDuration(t.Attrs[AttrDuration]) }

// StreamsOfType returns the streams whose type is typ ("Video", "Audio", "Subtitles").
func (t Title) StreamsOfType(typ string) []Stream {
	var out []Stream
	for _, s := range t.Streams {
		if s.Type() == typ {
			out = append(out, s)
		}
	}
	return out
}

func parseDuration(s string) time.Duration {
	parts := strings.Split(s, ":")
	if s == "" || len(parts) > 3 {
		return 0
	}
	var total time.Duration
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		total = total*60 + time.Duration(n)
	}
	return total * time.Second
}

// Info is the parsed output of `makemkvcon -r info <source>`.
type Info struct {
	Version    string // e.g. "MakeMKV v2.0.0 linux(x64-release)"
	Drives     []Drive
	Messages   []Message
	Disc       Disc
	TitleCount int // from TCOUNT; -1 if absent
	Titles     []Title
}

// Message code MakeMKV emits once at startup with the version string.
const msgStarted = 1005

// ParseInfo reads robot-mode info output.
func ParseInfo(r io.Reader) (*Info, error) {
	info := &Info{Disc: Disc{Attrs: Attrs{}}, TitleCount: -1}
	titles := map[int]*Title{}
	streams := map[[2]int]*Stream{}

	title := func(id int) *Title {
		t, ok := titles[id]
		if !ok {
			t = &Title{ID: id, Attrs: Attrs{}}
			titles[id] = t
		}
		return t
	}

	err := ScanRecords(r, func(rec Record) {
		f := rec.Fields
		switch rec.Kind {
		case "MSG":
			if len(f) < 4 {
				return
			}
			m := Message{Code: atoi(f[0]), Flags: atoi(f[1]), Text: f[3]}
			if len(f) > 4 {
				m.Format = f[4]
				m.Params = f[5:]
			}
			if m.Code == msgStarted && len(m.Params) > 0 {
				info.Version = m.Params[0]
			}
			info.Messages = append(info.Messages, m)
		case "DRV":
			if len(f) < 7 {
				return
			}
			info.Drives = append(info.Drives, Drive{
				Index: atoi(f[0]), State: DriveState(atoi(f[1])), Flags: DiscFlags(atoi(f[3])),
				DriveName: f[4], DiscLabel: f[5], Device: f[6],
			})
		case "TCOUNT":
			if len(f) >= 1 {
				info.TitleCount = atoi(f[0])
			}
		case "CINFO":
			if len(f) >= 3 {
				info.Disc.Attrs[atoi(f[0])] = f[2]
			}
		case "TINFO":
			if len(f) >= 4 {
				title(atoi(f[0])).Attrs[atoi(f[1])] = f[3]
			}
		case "SINFO":
			if len(f) >= 5 {
				key := [2]int{atoi(f[0]), atoi(f[1])}
				s, ok := streams[key]
				if !ok {
					s = &Stream{ID: key[1], Attrs: Attrs{}}
					streams[key] = s
				}
				s.Attrs[atoi(f[2])] = f[4]
			}
		}
	})
	if err != nil {
		return nil, err
	}

	for key, s := range streams {
		t := title(key[0])
		t.Streams = append(t.Streams, *s)
	}
	for _, t := range titles {
		sort.Slice(t.Streams, func(i, j int) bool { return t.Streams[i].ID < t.Streams[j].ID })
		info.Titles = append(info.Titles, *t)
	}
	sort.Slice(info.Titles, func(i, j int) bool { return info.Titles[i].ID < info.Titles[j].ID })
	return info, nil
}

// MainFeature returns the largest title by size, breaking ties by duration
// and then by lowest title ID.
func MainFeature(titles []Title) (Title, bool) {
	if len(titles) == 0 {
		return Title{}, false
	}
	best := titles[0]
	for _, t := range titles[1:] {
		switch {
		case t.SizeBytes() > best.SizeBytes():
			best = t
		case t.SizeBytes() == best.SizeBytes() && t.Duration() > best.Duration():
			best = t
		}
	}
	return best, true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
