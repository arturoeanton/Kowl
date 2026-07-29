# Kowl

Watch files, run JavaScript when they change.

Filesystem events come from [fsnotify](https://github.com/fsnotify/fsnotify); the scripts
run on [otto](https://github.com/robertkrimen/otto), an embedded ES5 interpreter. There is
no Node.js, no npm and no `require` — one static binary and one `.js` file.

*[Léeme en español](README.es.md)*

[![asciicast](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf.svg)](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf)

- [Quick start](#quick-start)
- [Choosing what to watch](#choosing-what-to-watch)
- [Hooks](#hooks)
- [How events reach your hooks](#how-events-reach-your-hooks)
- [Script API](#script-api)
- [Output and operations](#output-and-operations)
- [Command line reference](#command-line-reference)
- [Building and testing](#building-and-testing)
- [Notes and limits](#notes-and-limits)

## Quick start

```
go build -o kowl .
```

Write the hooks you care about:

```js
// watch.js
function write(name, op, event) {
    console.log("changed:", event.name, event.size + " bytes")
}
```

Point Kowl at a file:

```
$ ./kowl -f /tmp/notes.txt -j watch.js
2026-07-29T10:15:04-03:00 info  watching /tmp/notes.txt with watch.js (hooks: write)
2026-07-29T10:15:09-03:00 info  changed: notes.txt 12 bytes
```

Kowl runs until interrupted and stops cleanly on Ctrl-C or `SIGTERM`.

## Choosing what to watch

`-f` takes a file, a directory or a glob, and may be repeated. Each matching path gets its
own watcher, and new matches are picked up as they appear.

```
./kowl -f /tmp/foo -j watch.js                        # one file
./kowl -f 'logs/*.log' -f /etc/hosts -j watch.js      # a glob and a file
./kowl -f ./config -j watch.js                        # a directory and its files
./kowl -f ./src -r -j watch.js                        # and the whole tree below it
./kowl -f . -r -x node_modules -x .git -j watch.js    # minus the noise
```

**Directories.** Watching a directory reports events for the files inside it. That is the
reliable way to catch editors that save by writing a new file and renaming it over the old
one, which no watch on the original inode will ever see.

**Recursion.** fsnotify does not recurse, so watching a directory only covers its direct
children. `-r` enumerates the tree and watches every directory in it, including
subdirectories created later. Symlinks are not followed, so a link pointing back up the
tree cannot loop.

**Excluding.** `-x` skips paths and is repeatable. A pattern with no separator is matched
against the base name, so `-x node_modules` covers that directory wherever in the tree it
turns up; one with a separator is matched against the whole path, so `-x '/srv/app/tmp/*'`
stays specific to that place. With `-r`, an excluded directory is not descended into at
all — the difference between skipping `node_modules` and skipping only its top level.

**Limits.** `--max-watches` caps how many paths are watched at once, so a recursive watch
over a large tree cannot exhaust the process's file descriptors. The search stops as soon
as the limit is reached rather than enumerating the whole tree and discarding most of it,
and reaching it is reported once, naming a path that was left out.

**Polling.** Alongside the watcher, Kowl polls every `-m` interval, which is what produces
`ticker` and `not_found`. `-m 0` disables polling, `-w` disables the watcher. Both at once
is rejected: nothing would be observing anything.

## Hooks

A script implements only the hooks it cares about. Kowl reports at startup which ones it
found, so a typo shows up immediately, and refuses to start if the script defines none of
them or does not parse.

| Hook | When it runs |
| --- | --- |
| `exist(name, op, event)` | a watched path was found and a watcher attached to it |
| `create(name, op, event)` | a file was created |
| `write(name, op, event)` | a file was written |
| `remove(name, op, event)` | a file was removed |
| `rename(name, op, event)` | a file was renamed |
| `chmod(name, op, event)` | a file's mode changed |
| `ticker(name, op, event)` | polling found the path, once per `-m` interval |
| `not_found(name, op, event)` | polling found nothing matching the pattern |

Every hook takes the same three arguments. `name` is the path the event is about, `op` is
the operation in uppercase (`WRITE`, `NOT_FOUND`, …), and `event` describes the path as it
stands when the hook runs:

| Field | |
| --- | --- |
| `event.path` | the full path, same as `name` |
| `event.op` | the operation, same as `op` |
| `event.name` | the base name |
| `event.dir` | the containing directory |
| `event.exists` | whether the path is there right now |
| `event.isDir` | whether it is a directory |
| `event.size` | size in bytes, `0` when it is gone |
| `event.modTime` | RFC 3339 timestamp, `""` when it is gone |

On a `REMOVE`, or when a rename beat the hook to it, the path is already gone by the time
the hook runs. `event.exists` is what says whether the rest of the fields mean anything.

```js
function write(name, op, event) {
    if (event.size > 1024 * 1024) {
        kWarn(event.name, "is getting large")
    }
}
```

See `example.js` for one of each.

### Lifecycle

Hooks never run concurrently, so a script can keep state in ordinary globals:

```js
var writes = 0
function write(name, op, event) {
    writes = writes + 1
    console.log(event.name, "written", writes, "times")
}
```

The VM is kept between events and reloaded when the script file changes, so edits take
effect without a restart — and reset those globals.

`SIGHUP` reloads it too, and reports what the fresh copy defines. That covers what
watching the file cannot: a script whose behaviour depends on the environment, or on
something it read at load time.

```
kill -HUP $(pgrep kowl)
```

A hook that runs longer than `--hook-timeout` is interrupted and its VM discarded; the
next event starts from a freshly loaded script. The same limit covers the script's top
level, so a loop among the statements outside your functions is reported rather than left
to hang the process.

## How events reach your hooks

### Backpressure

Events are queued and run one at a time, in order. Nothing upstream waits for a hook: the
fsnotify readers and the code that starts and stops watchers keep going while one runs, so
a slow hook cannot stall watch bookkeeping or back the kernel's event queue up behind it.

The queue is bounded. A hook that never keeps up eventually costs events, and Kowl says so
rather than blocking the readers and letting the kernel drop them where nobody can see it
happen. If you see `hooks cannot keep up`, the hook is too slow for the rate of change,
not the other way round.

### Debouncing

One save from an editor usually produces several write events. Kowl collapses write,
create and chmod events over a quiet period so the hook runs once, after the file has
settled. `--debounce 0` runs on every event instead.

### Self-triggering

A hook that writes a watched file would otherwise wake itself again, and two hooks that
write each other's files would wake each other forever. Kowl records every path a hook
changed through the helpers below, and ignores the events those changes produce while the
path is still exactly in the state the hook left it in. So this terminates on its own:

```js
function write(name, op, event) {
    kStringToFile("port=8080", event.path)
}
```

Writes made through `kExec` leave no such record. For the file the hook was woken for,
Kowl falls back to comparing it before and after, which cannot tell a hook's write apart
from one someone else made at the same moment. Prefer the helpers where that distinction
matters.

`--self-trigger` turns the whole thing off, for a script that really does want to react to
its own writes.

## Script API

Helpers throw a JavaScript exception on failure and return the value on success, so a hook
that ignores an error stops instead of continuing with bad data. `try`/`catch` to handle
one yourself.

### Running commands

> `kExec(name, ...args, [options]) -> {stdout, stderr, code, truncated}`

```js
var out = kExec("ls", "-l")
console.log(out.stdout)
```

A command that runs and exits non-zero is **not** an error: `code` holds the exit status,
and `stdout` and `stderr` hold whatever it produced. Only a command that could not be run
at all, or that outlived `--exec-timeout`, throws. Output beyond `--max-output` is dropped
and `truncated` is set.

```js
var out = kExec("curl", "-s", "https://example.com")
if (out.code !== 0) {
    kError("curl failed:", out.stderr)
}
```

A trailing object is options rather than another argument:

```js
kExec("git", "status", "--short", {
    dir:   "/srv/repo",             // working directory
    env:   {LANG: "C"},             // added to Kowl's environment, overriding on conflict
    stdin: "input for the command"
})
```

`env` is added to the environment Kowl already has rather than replacing it, so a command
never silently loses `PATH`. Arguments go to the command directly, with no shell in
between: use `kExec("sh", "-c", ...)` if you want one, and remember what that means for
anything interpolated into the string.

### Reading and writing files

> `kFileToString(path) -> string`
> `kStringToFile(value, path)` — replaces the contents, creating the file if needed
> `kAppendFile(value, path)` — appends, creating the file if needed
> `kRemoveFile(path)` — errors when the path is not there

### Inspecting the filesystem

> `kFileExists(path) -> boolean`
> `kStat(path) -> {path, name, dir, size, mode, modTime, isDir}`
> `kListDir(path) -> [{name, path, size, isDir}, ...]`
> `kGlob(pattern) -> [path, ...]`

`kStat` throws when the path is not there, so `kFileExists` is the way to ask without
handling an exception. `kListDir` is sorted by name, and `kGlob` returns an empty array
rather than null when nothing matches.

```js
function write(name, op, event) {
    var entries = kListDir(event.dir)
    for (var i = 0; i < entries.length; i++) {
        if (!entries[i].isDir && entries[i].size === 0) {
            kWarn(entries[i].name, "is empty")
        }
    }
}
```

### Changing the filesystem

> `kMkdirAll(path)` — creates every parent it needs, and does not mind an existing one
> `kRemoveAll(path)` — deletes a whole tree, and does not mind a path that never existed
> `kCopyFile(source, destination)` — preserves permissions, replaces the destination
> `kMoveFile(source, destination)` — renames, copying when it must cross a filesystem

### Encryption

> `kEncrypt(passphrase, plaintext) -> string`
> `kDecrypt(passphrase, ciphertext) -> string`

```js
var sealed = kEncrypt("passphrase", "plain text")
console.log(kDecrypt("passphrase", sealed))
```

The passphrase is stretched with PBKDF2-HMAC-SHA256 and the payload sealed with
AES-256-GCM, so a modified ciphertext is rejected rather than silently decrypted. A fresh
salt and nonce per call mean the same input never produces the same output. Key derivation
is deliberately slow; do not call these once per event on a busy watch.

### HTTP

Via [gentleman](https://github.com/h2non/gentleman), exposed as `kCli`:

```js
kCli.URL("http://httpbin.org")
var req = kCli.Request()
req.Path("/headers")
req.SetHeader("Client", "kowl")
var res = req.Send()
console.log("Body:", res[0].String())
```

```js
kCli.URL("http://httpbin.org/post")
var req = kCli.Request()
req.Method("POST")
req.Use(kBodyJSON({"foo": "bar"}))
var res = req.Send()
console.log("Status:", res[0].StatusCode)
```

`kBodyJSON`, `kBodyXML` and `kBodyString` build request bodies. Requests time out after
`--http-timeout`. These are Go objects exposed directly, so `Send()` returns Go's
`(response, error)` pair as a two-element array.

### Logging

> `kDebug(...)` `kLog(...)` `kWarn(...)` `kError(...)`

`console.log`, `console.info`, `console.debug`, `console.warn` and `console.error` are
wired to the same place. Everything a script logs goes through Kowl's own output —
timestamped, leveled, on stderr, filtered by `--log-level`. Arguments are joined with
spaces and objects print as their contents.

### Waiting

> `kSleep(duration)`

```js
kSleep("250ms")   // a duration string
kSleep(250)       // or a number of milliseconds
```

The wait counts against `--hook-timeout`, and asking for longer is refused rather than
quietly shortened: a hook interrupted part way through is worse than one that never
started. Hooks are serialised, so sleeping in one holds up the rest.

### Operating system

> `kGetEnv` `kSetEnv` `kHostname` `kGetpid` `kGetppid` `kGetgid` `kGetuid` `kGetegid`
> `kArgs` `kNow`

```js
kSetEnv("VAR", "data")
console.log(kGetEnv("VAR"), kHostname(), kNow())
```

### underscore.js

[underscore](https://underscorejs.org) is available as `_`:

```js
var stooges = [{name: 'moe', age: 40}, {name: 'larry', age: 50}]
console.log(_.pluck(stooges, 'name'))
```

## Output and operations

Everything Kowl and its scripts report goes to **stderr**, timestamped and leveled:

```
2026-07-29T10:15:04-03:00 info  watching /tmp/foo with watch.js (hooks: exist, write)
2026-07-29T10:15:07-03:00 info  WRITE /tmp/foo 12 bytes
```

`--log-format json` writes one object per line instead, for a collector to pick up:

```json
{"time":"2026-07-29T10:15:07-03:00","level":"info","message":"WRITE /tmp/foo 12 bytes"}
```

A failure that keeps repeating — a script that no longer parses, say — is reported once
and then counted, so it cannot bury everything else.

**Signals.** `SIGINT` and `SIGTERM` shut down cleanly. `SIGHUP` reloads the script.

**Exit codes.** `0` on a clean shutdown or `--help`, `1` when the script cannot be loaded,
`2` on a usage error.

## Command line reference

```
Usage:
  kowl [OPTIONS]

Application Options:
  -f, --filename=       file, directory or glob to observe, repeatable
  -j, --javascript=     JavaScript file holding the hooks
  -m, --interval=       poll interval, 0 disables polling (default: 1s)
  -w, --flagNotWatcher  disable the filesystem watcher, leaving only polling
  -r, --recursive       watch every directory below a matched directory
      --max-watches=    how many paths may be watched at once (default: 4096)
  -x, --exclude=        skip matching paths, repeatable; no separator matches
                        the base name
      --debounce=       quiet period before a burst of write events runs a
                        hook, 0 disables (default: 200ms)
      --self-trigger    let a hook that writes an observed file wake itself
                        again
      --hook-timeout=   how long a hook may run before it is interrupted
                        (default: 30s)
      --exec-timeout=   how long a kExec command may run (default: 60s)
      --http-timeout=   how long a kCli request may take (default: 30s)
      --max-output=     bytes of stdout and of stderr kept per kExec command
                        (default: 1048576)
      --log-level=      debug, info, warn or error (default: info)
      --log-format=     text or json (default: text)
  -V, --version         print the version and exit

Help Options:
  -h, --help            Show this help message
```

Every timing flag takes a duration: `-m 5s`, `--debounce 50ms`, `--hook-timeout 2m`. A
bare number is rejected rather than assumed to mean some particular unit. Kowl takes no
positional arguments; every path needs its own `-f`.

## Building and testing

```
go build -o kowl .
go build -ldflags "-X main.version=1.2.0" -o kowl .   # stamp a release version
```

Dependencies are pinned in `go.mod`. Without a stamped version, `kowl --version` falls
back to the commit Go records in the binary, and says so when the tree was dirty.

```
go test ./...                  # everything
go test -race ./...            # the concurrency cover
go test -short ./...           # skips the tests that build the binary
go test -run TestDispatch .    # one group
go test -v -run TestEncrypt ./js
```

## Notes and limits

* fsnotify uses inotify on Linux, kqueue on macOS and the BSDs, and
  ReadDirectoryChangesW on Windows. The tests assume a Unix shell.
* Watchers follow inodes, not paths. Kowl rebuilds one when the file it was watching is
  deleted and recreated, which it notices within a second.
* If the kernel's own queue overflows, events are lost before Kowl ever sees them. That is
  reported, and the watcher for the affected path is rebuilt so the path is announced
  again with `EXIST` — a script's cue to resynchronise from whatever it tracks.
* `kExec` runs whatever a script asks it to, with the privileges of the Kowl process.
  Treat the script file as trusted input.
* There is no CI. `go vet`, `gofmt -l` and `go test -race ./...` are the bar.
