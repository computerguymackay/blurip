package rip

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// genericWords are disc-label words that say nothing about what is on the
// disc ("DVD_VIDEO", "BDROM", "DISC 1", "Untitled disc").
var genericWords = map[string]bool{
	"DVD": true, "VIDEO": true, "DVDVIDEO": true, "DVDVOLUME": true, "VOLUME": true, "TS": true,
	"BD": true, "BDROM": true, "BDMV": true, "BLURAY": true, "BLU": true, "RAY": true, "ROM": true,
	"CD": true, "CDROM": true, "UDF": true, "DISC": true, "DISK": true, "MOVIE": true, "FILM": true,
	"FEATURE": true, "MAIN": true, "NO": true, "NAME": true, "UNTITLED": true, "LABEL": true,
	"NEW": true, "LOGICAL": true, "ID": true, "WS": true, "FS": true,
}

// tagWords are language and region tags ("EN_100613", "UK_PAL"). Alone
// they could be a title ("It", "Us"), so they count for nothing either
// way, like a short number.
var tagWords = map[string]bool{
	"EN": true, "ENG": true, "GB": true, "UK": true, "US": true, "USA": true, "FR": true, "FRA": true,
	"DE": true, "GER": true, "ES": true, "SPA": true, "IT": true, "ITA": true, "NL": true, "SE": true,
	"DK": true, "FI": true, "PL": true, "EU": true, "AU": true, "JP": true, "PAL": true,
	"NTSC": true, "NORDIC": true, "RETAIL": true, "RENTAL": true,
}

// LooksLikeCode reports whether a disc name looks like a serial number or
// a generic label ("SBK0X7AJ", "D1234567", "DVD_VIDEO", "Untitled disc")
// rather than a title, so the user should be asked what the disc is.
// A false alarm only costs a question that Enter answers.
func LooksLikeCode(name string) bool {
	words := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	junk := len(words) == 0
	for _, w := range words {
		switch {
		case genericWords[w] || codeWord(w):
			junk = true
		case strings.Trim(w, "0123456789") == "" || tagWords[w]:
			// a short number ("Disc 1", "1917") or a tag says nothing either way
		default:
			return false // a real word
		}
	}
	return junk
}

// codeWord: letters mixed with digits ("SBK0X7AJ", "D1", "UPB1"), or a long
// run of digits ("20231115"). Plain numbers such as "300" or "1917" are
// titles.
func codeWord(w string) bool {
	letters, digits := 0, 0
	for _, r := range w {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsLetter(r):
			letters++
		}
	}
	return digits > 0 && (letters > 0 || digits >= 5)
}

// Rename renames a finished rip (not a box set's) from oldName to newName:
// the folder, and the file if it is named after the folder. The folder is
// never merged into an existing one: " (2)" is added instead. On failure
// the rip is left as it was.
func Rename(res Result, oldName, newName string) (Result, error) {
	safe := SafeName(newName)
	if safe == "" {
		return res, fmt.Errorf("%q can't be used as a folder name", newName)
	}
	newName = safe
	if filepath.Base(res.Dir) == newName {
		return res, nil
	}
	out := Result{Name: newName, Bytes: res.Bytes, Files: append([]string(nil), res.Files...)}
	dest, err := moveIntoPlace(res.Dir, filepath.Dir(res.Dir), newName)
	if err != nil {
		return res, err
	}
	out.Dir = dest
	for i, f := range out.Files {
		if f != oldName+".mkv" {
			continue
		}
		want := newName + ".mkv"
		if _, err := os.Lstat(filepath.Join(dest, want)); err == nil {
			continue
		}
		if err := os.Rename(filepath.Join(dest, f), filepath.Join(dest, want)); err == nil {
			out.Files[i] = want
		}
	}
	return out, nil
}
