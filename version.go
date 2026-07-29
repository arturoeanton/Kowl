package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=1.2.0" -o kowl .
//
// Left alone, the revision Go records in the binary is used instead, so a plain
// `go build` still produces something identifiable.
var version = ""

// versionString describes this build in one line.
func versionString() string {
	name, revision, modified := buildDetails()

	parts := []string{"kowl"}
	switch {
	case version != "":
		parts = append(parts, version)
	case revision != "":
		parts = append(parts, shortRevision(revision))
	default:
		parts = append(parts, "(devel)")
	}
	if modified {
		parts = append(parts, "(with uncommitted changes)")
	}
	parts = append(parts, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
	if name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, " ")
}

// buildDetails reads what the toolchain recorded in the binary: the module path, the
// commit it was built from, and whether the tree was dirty at the time.
func buildDetails() (module, revision string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", false
	}
	module = info.Main.Path
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return module, revision, modified
}

// shortRevision abbreviates a commit hash the way git does.
func shortRevision(revision string) string {
	const short = 12
	if len(revision) > short {
		return revision[:short]
	}
	return revision
}

// wantsVersion reports whether -V or --version appears before any -- separator.
//
// It is checked before the flags are parsed, because -f and -j are required and asking
// a program for its version should not require telling it what to watch.
func wantsVersion(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-V" || arg == "--version" {
			return true
		}
	}
	return false
}
