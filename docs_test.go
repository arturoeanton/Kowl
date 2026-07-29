package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// readmes are the documents these tests keep honest.
var readmes = []string{"README.md", "README.es.md"}

func readDoc(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// helperPattern finds the k-prefixed names the documents mention.
var helperPattern = regexp.MustCompile(`\bk[A-Z][A-Za-z]*\b`)

// Documenting a helper that does not exist is worse than not documenting it.
func TestREADMEsOnlyMentionRealHelpers(t *testing.T) {
	vm := newVM(defaultVMConfig())

	for _, name := range readmes {
		t.Run(name, func(t *testing.T) {
			mentioned := map[string]bool{}
			for _, match := range helperPattern.FindAllString(readDoc(t, name), -1) {
				mentioned[match] = true
			}
			if len(mentioned) < 20 {
				t.Fatalf("only found %d helper names, the pattern is probably wrong", len(mentioned))
			}

			for helper := range mentioned {
				if goja.IsUndefined(vm.Get(helper)) {
					t.Errorf("%s documents %s, which is not defined in a VM", name, helper)
				}
			}
		})
	}
}

// Every helper a script can call should be findable in the documentation.
func TestREADMEsDocumentEveryHelper(t *testing.T) {
	english := readDoc(t, "README.md")
	spanish := readDoc(t, "README.es.md")

	vm := newVM(defaultVMConfig())
	for _, helper := range helperPattern.FindAllString(strings.Join(bindingNames(), " "), -1) {
		if !strings.Contains(english, helper) {
			t.Errorf("README.md does not mention %s", helper)
		}
		if !strings.Contains(spanish, helper) {
			t.Errorf("README.es.md does not mention %s", helper)
		}
		if goja.IsUndefined(vm.Get(helper)) {
			t.Errorf("%s is listed as a binding but is not defined", helper)
		}
	}
}

// bindingNames lists what newVM installs, by asking a VM rather than by keeping a second
// list that could drift.
func bindingNames() []string {
	vm := newVM(defaultVMConfig())
	names, err := vm.RunString(`Object.getOwnPropertyNames(this).filter(n => /^k[A-Z]/.test(n)).sort().join(" ")`)
	if err != nil {
		return nil
	}
	return strings.Fields(names.String())
}

// codeBlock captures fenced JavaScript examples.
var codeBlock = regexp.MustCompile("(?s)```js\\n(.*?)```")

// An example that does not parse is a broken promise, and readers copy these.
func TestREADMEJavaScriptExamplesParse(t *testing.T) {
	for _, name := range readmes {
		t.Run(name, func(t *testing.T) {
			blocks := codeBlock.FindAllStringSubmatch(readDoc(t, name), -1)
			if len(blocks) < 10 {
				t.Fatalf("found only %d JavaScript examples, the pattern is probably wrong", len(blocks))
			}
			for i, block := range blocks {
				if _, err := goja.Compile(name, block[1], true); err != nil {
					t.Errorf("%s example %d does not parse: %v\n%s", name, i+1, err, block[1])
				}
			}
		})
	}
}

// headingAnchor mimics how GitHub turns a heading into a fragment identifier.
func headingAnchor(heading string) string {
	anchor := strings.ToLower(strings.TrimSpace(heading))
	anchor = regexp.MustCompile("[^\\p{L}\\p{N}\\s-]").ReplaceAllString(anchor, "")
	return strings.ReplaceAll(anchor, " ", "-")
}

var (
	headingLine  = regexp.MustCompile(`(?m)^#{1,6}\s+(.*)$`)
	internalLink = regexp.MustCompile(`\]\(#([^)]+)\)`)
	relativeLink = regexp.MustCompile(`\]\((?:\./)?([A-Za-z0-9_./-]+\.(?:md|js|go))\)`)
)

// A table of contents that points at headings which no longer exist is worse than none.
func TestREADMEInternalLinksResolve(t *testing.T) {
	for _, name := range readmes {
		t.Run(name, func(t *testing.T) {
			doc := readDoc(t, name)

			anchors := map[string]bool{}
			for _, heading := range headingLine.FindAllStringSubmatch(doc, -1) {
				anchors[headingAnchor(heading[1])] = true
			}

			links := internalLink.FindAllStringSubmatch(doc, -1)
			if len(links) < 5 {
				t.Fatalf("found only %d internal links, the pattern is probably wrong", len(links))
			}
			for _, link := range links {
				if !anchors[link[1]] {
					t.Errorf("%s links to #%s, which no heading produces", name, link[1])
				}
			}
		})
	}
}

// A link to a file that is not in the repository is a dead end.
func TestREADMERelativeLinksExist(t *testing.T) {
	for _, name := range readmes {
		t.Run(name, func(t *testing.T) {
			for _, link := range relativeLink.FindAllStringSubmatch(readDoc(t, name), -1) {
				if _, err := os.Stat(link[1]); err != nil {
					t.Errorf("%s links to %s, which is not in the repository", name, link[1])
				}
			}
		})
	}
}

// The options block is copied from --help, so it goes stale silently.
func TestREADMEOptionsMatchTheRealHelp(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	binary := filepath.Join(t.TempDir(), "kowl")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("building kowl: %v\n%s", err, out)
	}
	// -h is a successful invocation, and the help goes to stdout.
	help, err := exec.Command(binary, "-h").Output()
	if err != nil {
		t.Fatalf("kowl -h: %v", err)
	}

	start := strings.Index(string(help), "Usage:")
	if start < 0 {
		t.Fatalf("help output has no usage block:\n%s", help)
	}
	real := strings.TrimSpace(string(help)[start:])

	for _, name := range readmes {
		doc := readDoc(t, name)
		if !strings.Contains(doc, real) {
			t.Errorf("%s does not contain the current help output; it should be:\n%s", name, real)
		}
	}
}

// The bundled example is the one script a reader is told to look at.
func TestExampleScriptParsesAndDefinesEveryHook(t *testing.T) {
	source, err := os.ReadFile("example.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goja.Compile("example.js", string(source), true); err != nil {
		t.Fatalf("example.js does not parse: %v", err)
	}

	hooks, err := NewRunner("example.js").DefinedHooks()
	if err != nil {
		t.Fatalf("loading example.js: %v", err)
	}
	if got, want := strings.Join(hooks, ","), strings.Join(hookNames, ","); got != want {
		t.Fatalf("example.js defines %q, want %q", got, want)
	}
}
