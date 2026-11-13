package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is set at build time: -ldflags "-X main.version=v1.0.0".
var version = ""

func versionString() string {
	v, rev, dirty := version, "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if v == "" {
		v = "dev"
		if len(rev) >= 7 {
			v += " (" + rev[:7]
			if dirty {
				v += ", modified"
			}
			v += ")"
		}
	}
	return fmt.Sprintf("blurip %s %s/%s %s", v, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
