// Package buildinfo reports which build of TestSync is running.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// ProtocolVersion is the WebSocket protocol version this server speaks. See
// PROTOCOL.md.
const ProtocolVersion = 1

// DocsURL is where a person who found a TestSync server can read about it.
const DocsURL = "https://github.com/paulsgrudups/TestSync"

// version is set at link time by a release build:
//
//	go build -ldflags "-X github.com/paulsgrudups/testsync/internal/buildinfo.version=v0.1.0"
//
// It is the one package variable in the server that is not a sentinel, and it
// is written only by the linker, never at run time.
var version = ""

// Info describes the running build.
type Info struct {
	// Version is the release version, or "dev" for a build that was not
	// stamped with one.
	Version string `json:"version"`

	// Commit is the VCS revision the binary was built from, when the Go
	// toolchain recorded one. It ends in "-dirty" for a build of a modified
	// tree.
	Commit string `json:"commit"`

	// GoVersion is the toolchain that built the binary.
	GoVersion string `json:"go_version"`

	// Protocol is [ProtocolVersion].
	Protocol int `json:"protocol"`
}

// Get returns the running build's details. It reads only values fixed at
// build time, so it is safe to call from any goroutine.
func Get() Info {
	info := Info{
		Version:   version,
		GoVersion: runtime.Version(),
		Protocol:  ProtocolVersion,
	}

	if info.Version == "" {
		info.Version = "dev"
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}

	var modified bool

	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Commit = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if info.Commit != "" && modified {
		info.Commit += "-dirty"
	}

	// A binary installed with "go install ...@v0.1.0" carries its module
	// version even without -ldflags.
	if info.Version == "dev" && build.Main.Version != "" && build.Main.Version != "(devel)" {
		info.Version = build.Main.Version
	}

	return info
}

// Descriptor is the body of GET / on both of the server's ports: what this
// server is, which protocol it speaks, and where to read about it. It replaces
// two placeholder strings that were the first thing a new user saw (API-4).
type Descriptor struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	Docs     string `json:"docs"`
}

// Describe returns the running server's descriptor.
func Describe() Descriptor {
	info := Get()

	return Descriptor{
		Service:  "testsync",
		Version:  info.Version,
		Protocol: info.Protocol,
		Docs:     DocsURL,
	}
}
