// Package makemkv parses the robot-mode output (-r) of makemkvcon.
package makemkv

import (
	"bufio"
	"io"
	"strings"
)

// Record is a single line of robot-mode output, e.g.
// `TINFO:0,9,0,"1:47:12"` → Kind "TINFO", Fields ["0","9","0","1:47:12"].
type Record struct {
	Kind   string
	Fields []string
}

// ParseRecord parses one robot-mode line. It returns false for lines that
// are not records (blank lines, stray text).
func ParseRecord(line string) (Record, bool) {
	line = strings.TrimRight(line, "\r\n")
	kind, rest, ok := strings.Cut(line, ":")
	if !ok || kind == "" || strings.ToUpper(kind) != kind || strings.ContainsAny(kind, " \"") {
		return Record{}, false
	}
	return Record{Kind: kind, Fields: splitFields(rest)}, true
}

// splitFields splits a comma-separated field list. Quoted fields may contain
// commas and quotes; a quoted field ends at a quote followed by a comma or
// end of line.
func splitFields(s string) []string {
	var out []string
	i := 0
	for {
		if i < len(s) && s[i] == '"' {
			j := i + 1
			for {
				k := strings.IndexByte(s[j:], '"')
				if k < 0 { // unterminated: take the rest
					return append(out, s[i+1:])
				}
				end := j + k
				if end+1 == len(s) {
					return append(out, s[i+1:end])
				}
				if s[end+1] == ',' {
					out = append(out, s[i+1:end])
					i = end + 2
					break
				}
				j = end + 1
			}
			continue
		}
		k := strings.IndexByte(s[i:], ',')
		if k < 0 {
			return append(out, s[i:])
		}
		out = append(out, s[i:i+k])
		i += k + 1
	}
}

// ScanRecords calls fn for every record read from r.
func ScanRecords(r io.Reader, fn func(Record)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if rec, ok := ParseRecord(sc.Text()); ok {
			fn(rec)
		}
	}
	return sc.Err()
}
