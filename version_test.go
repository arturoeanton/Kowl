package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionStringNamesTheProgramAndToolchain(t *testing.T) {
	got := versionString()

	if !strings.HasPrefix(got, "kowl ") {
		t.Fatalf("versionString = %q, want it to start with the program name", got)
	}
	for _, want := range []string{runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(got, want) {
			t.Fatalf("versionString = %q, missing %q", got, want)
		}
	}
}

// A plain `go build` stamps no version, so the recorded revision stands in for one.
// Either way there must be something after the program name.
func TestVersionStringAlwaysIdentifiesTheBuild(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = ""
	if fields := strings.Fields(versionString()); len(fields) < 2 || fields[1] == "" {
		t.Fatalf("versionString = %q, want an identifier after the name", versionString())
	}

	version = "9.9.9"
	if got := versionString(); !strings.Contains(got, "9.9.9") {
		t.Fatalf("versionString = %q, want the stamped version", got)
	}
}

func TestShortRevisionAbbreviatesAHash(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1234567890abcdef1234567890abcdef12345678", "1234567890ab"},
		{"abc123", "abc123"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := shortRevision(tt.in); got != tt.want {
			t.Fatalf("shortRevision(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWantsVersion(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"long form", []string{"--version"}, true},
		{"short form", []string{"-V"}, true},
		{"among other flags", []string{"-f", "x", "--version", "-j", "y"}, true},
		{"absent", []string{"-f", "x", "-j", "y"}, false},
		{"no arguments", nil, false},
		{"after a separator is not the flag", []string{"--", "--version"}, false},
		{"lowercase -v is not the flag", []string{"-v"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsVersion(tt.args); got != tt.want {
				t.Fatalf("wantsVersion(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// Asking for the version must not require -f and -j, which are otherwise required.
func TestVersionFlagExitsSuccessfullyWithoutOtherFlags(t *testing.T) {
	for _, flag := range []string{"--version", "-V"} {
		t.Run(flag, func(t *testing.T) {
			code, stdout, stderr := invoke(t, flag)

			if code != exitOK {
				t.Fatalf("%s exit code = %d, want %d (stderr: %s)", flag, code, exitOK, stderr)
			}
			if !strings.HasPrefix(stdout, "kowl ") {
				t.Fatalf("%s printed %q, want the version line", flag, stdout)
			}
			if stderr != "" {
				t.Fatalf("%s wrote to stderr: %q", flag, stderr)
			}
		})
	}
}

func TestVersionAppearsInHelp(t *testing.T) {
	_, stdout, _ := invoke(t, "-h")

	if !strings.Contains(stdout, "--version") {
		t.Fatalf("help output does not mention --version:\n%s", stdout)
	}
}
