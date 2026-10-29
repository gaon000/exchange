// Package buildinfo records executable provenance without invoking Git at run time.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Info describes the executing binary, not a possibly different current checkout.
// Empty Revision and RevisionKnown=false explicitly mean unavailable VCS evidence.
type Info struct {
	GoVersion     string `json:"goVersion"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Revision      string `json:"revision"`
	RevisionKnown bool   `json:"revisionKnown"`
	Modified      bool   `json:"modified"`
}

// Current reads the build information embedded by the Go toolchain.
func Current() Info {
	i := Info{GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if b, ok := debug.ReadBuildInfo(); ok {
		for _, s := range b.Settings {
			switch s.Key {
			case "vcs.revision":
				i.Revision, i.RevisionKnown = s.Value, s.Value != ""
			case "vcs.modified":
				i.Modified = s.Value == "true"
			}
		}
	}
	return i
}
