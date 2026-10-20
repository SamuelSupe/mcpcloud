// Package buildinfo identifies the executable and its release source.
package buildinfo

import "runtime"

// Release builds set these values with -ldflags -X.
var (
	Version   = "0.1.1-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildTime: BuildTime, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
}
