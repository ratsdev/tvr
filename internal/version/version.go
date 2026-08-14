package version

import (
	"runtime/debug"
	"strings"
)

// Version and Commit are set at build time via -ldflags. Local builds fall
// back to the Go toolchain's module and VCS metadata.
var (
	Version = "dev"
	Commit  = ""
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		if strings.TrimSpace(Version) == "" {
			Version = "dev"
		}
		return
	}
	if Version == "dev" || Version == "" {
		if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
			Version = strings.TrimPrefix(v, "v")
		}
	}
	if Commit == "" {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				Commit = s.Value
				break
			}
		}
	}
	if strings.TrimSpace(Version) == "" {
		Version = "dev"
	}
}

// ShortCommit returns the first 7 characters of Commit, or empty if unset.
func ShortCommit() string {
	c := strings.TrimSpace(Commit)
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// Label is "{version}({commit})" when a commit is known, otherwise just version.
// Dev builds therefore show "dev(abc1234)" rather than a bare "dev".
func Label() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		v = "dev"
	}
	if c := ShortCommit(); c != "" {
		return v + "(" + c + ")"
	}
	return v
}
