# Kowl

Watch files, run JavaScript when they change.

Filesystem events come from [fsnotify](https://github.com/fsnotify/fsnotify); the scripts
run on [goja](https://github.com/dop251/goja), an embedded ES2015+ interpreter. There is
no Node.js, no npm and no `require` — one static binary and one `.js` file.

*[Léeme en español](README.es.md)*

- [Quick start](#quick-start)
- [What it is good for](#what-it-is-good-for)
- [Choosing what to watch](#choosing-what-to-watch)
- [Hooks](#hooks)
- [How events reach your hooks](#how-events-reach-your-hooks)
- [Script API](#script-api)
- [Output and operations](#output-and-operations)
- [When something looks wrong](#when-something-looks-wrong)
- [Command line reference](#command-line-reference)
- [Building and testing](#building-and-testing)
- [Notes and limits](#notes-and-limits)

## Quick start

```
go install github.com/arturoeanton/Kowl@latest
```

That puts a binary called `Kowl` in `$GOBIN`, named after the repository. Rename it if you
would rather type `kowl`.

Or from a clone:

```
go build -o kowl .
```

Write the hooks you care about:

```js
// watch.js
function write(name, op, event) {
    console.log(`changed: ${event.name} ${event.size} bytes`)
}
```

Point Kowl at a file:

```
$ ./kowl -f notes.txt -j watch.js
2026-07-29T13:00:28-03:00 info  watching notes.txt with watch.js (hooks: write)
2026-07-29T13:00:30-03:00 info  changed: notes.txt 13 bytes
```

Kowl runs until interrupted and stops cleanly on Ctrl-C or `SIGTERM`.

## What it is good for

The gap Kowl fills is between a shell one-liner and a daemon you have to write. A
`while true; do ...; done` loop has no debouncing, no error handling and no logging; a
purpose-built watcher in Go or Node is a project of its own. Kowl is a binary you drop
next to a script.

Every example below has been run as written.

### Run something when a file is saved

The obvious one — build, lint, test, reload. `--debounce` collapses the burst of events a
single save produces, so this runs once per save rather than three times on a half-written
file.

```js
function write(name, op, event) {
    if (!event.name.endsWith(".txt")) return
    const out = kExec("wc", "-l", event.path)
    if (out.code !== 0) {
        kError(`check failed: ${out.stderr.trim()}`)
        return
    }
    kLog(`ok: ${out.stdout.trim()}`)
}
```

```
./kowl -f src -r -j build.js -m 0 --debounce 100ms
```

### Process files dropped into a directory

The inbox pattern: something delivers files, you pick them up, act on them, move them
away. `create` fires on arrival, and moving the file out through `kMoveFile` does not wake
the hook again.

```js
function create(name, op, event) {
    if (event.isDir || !event.name.endsWith(".csv")) return
    kSleep("100ms")                                   // let the writer finish
    const rows = kFileToString(event.path).trim().split("\n").length
    kMoveFile(event.path, `${kGetEnv("DONE")}/${event.name}`)
    kLog(`processed ${event.name}: ${rows} rows`)
}
```

```
DONE=./done ./kowl -f inbox -j inbox.js -m 0
```

### Watch for something that stopped happening

`ticker` and `not_found` fire on the poll interval whether or not anything changed, which
is what makes an absence detectable. This is a dead man's switch over a heartbeat file:
noise-free while healthy, one line when it goes stale, one when it comes back.

```js
const STALE_MS = 60000
let alerted = false

function ticker(name, op, event) {
    const age = Date.now() - new Date(event.modTime).getTime()
    if (age > STALE_MS && !alerted) {
        kError(`heartbeat is ${Math.round(age / 1000)}s old`)
        alerted = true
    } else if (age <= STALE_MS && alerted) {
        kLog("heartbeat recovered")
        alerted = false
    }
}

function not_found(name, op, event) {
    if (!alerted) {
        kError("heartbeat file is missing")
        alerted = true
    }
}
```

```
./kowl -f /var/run/app.beat -j heartbeat.js -m 10s -w
```

### Reload a service when its configuration changes

Validate first, and only signal the service if the new file is good — a watcher that
reloads a broken config is worse than no watcher.

```js
function write(name, op, event) {
    const check = kExec("nginx", "-t", "-c", event.path)
    if (check.code !== 0) {
        kError(`config rejected, not reloading: ${check.stderr.trim()}`)
        return
    }
    kCopyFile(event.path, `${event.path}.good`)
    kExec("systemctl", "reload", "nginx")
    kLog("reloaded")
}
```

### Send changes somewhere else

`kCli` is an HTTP client, so a change can become a webhook, a metric or a message without
shelling out to `curl`.

```js
function create(name, op, event) {
    kCli.URL(kGetEnv("WEBHOOK"))
    const req = kCli.Request()
    req.Method("POST")
    req.Use(kBodyJSON({file: event.name, size: event.size, at: event.modTime}))
    const res = req.Send()
    if (res.statusCode >= 300) {
        kWarn(`webhook returned ${res.statusCode}`)
    }
}
```

Kowl is a poor fit when you need sub-millisecond reaction times, when the work per event
is heavy enough to want real concurrency, or when you are already inside a runtime that
watches files for you. It has no scheduler, no queue that survives a restart, and no
clustering.

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

**Names that look like patterns.** A pattern is used as a glob first. If it matches
nothing and a file with that exact name exists, that file is watched instead — the same
thing a shell does with an unmatched glob. That is what makes `-f 'report[1].pdf'` work,
which matters because it is how a browser names a second download. `-x` follows the same
rule. An empty pattern is rejected rather than quietly matching nothing.

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

See `example.js` for one of each hook.

### Lifecycle

Hooks never run concurrently, so a script can keep state in ordinary globals:

```js
let writes = 0
function write(name, op, event) {
    writes += 1
    console.log(`${event.name} written ${writes} times`)
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

The interrupt only takes effect between JavaScript statements, so it cannot reach a hook
sitting inside one of the helpers below — reading a fifo nobody writes to, or a file on a
mount that has stopped answering. A few seconds after the timeout such a hook is
**abandoned**: Kowl says so and goes back to handling events, but the hook is still out
there and may finish later. `kExec`, `kCli` and `kSleep` have their own limits and never
reach this.

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
const out = kExec("ls", "-l")
console.log(out.stdout)
```

A command that runs and exits non-zero is **not** an error: `code` holds the exit status,
and `stdout` and `stderr` hold whatever it produced. Only a command that could not be run
at all, or that outlived `--exec-timeout`, throws. Output beyond `--max-output` is dropped
and `truncated` is set.

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
    for (const entry of kListDir(event.dir)) {
        if (!entry.isDir && entry.size === 0) {
            kWarn(`${entry.name} is empty`)
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
const sealed = kEncrypt("passphrase", "plain text")
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
const req = kCli.Request()
req.Path("/headers")
req.SetHeader("Client", "kowl")
const res = req.Send()
console.log("Body:", res.String())
```

```js
kCli.URL("http://httpbin.org/post")
const req = kCli.Request()
req.Method("POST")
req.Use(kBodyJSON({foo: "bar"}))
console.log("Status:", req.Send().statusCode)
```

`kBodyJSON`, `kBodyXML` and `kBodyString` build request bodies. Requests time out after
`--http-timeout`, and `Send()` throws when one fails.

These are Go objects exposed directly. Their **fields** reach JavaScript in lower camel
case — `res.statusCode`, not `res.StatusCode` — while their **methods** keep their Go
names, so it is `res.String()` and `kCli.URL()`. The same rule produces the field names of
`event`, `kStat` and `kExec`.

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
const stooges = [{name: "moe", age: 40}, {name: "larry", age: 50}]
console.log(_.pluck(stooges, "name"))
```

It is vendored under `vendorjs/` and embedded in the binary. Most of what it offers is now
in the language itself, so new scripts rarely need it.

## Output and operations

Everything Kowl and its scripts report goes to **stderr**, timestamped and leveled:

```
2026-07-29T13:00:28-03:00 info  watching notes.txt with watch.js (hooks: write)
2026-07-29T13:00:30-03:00 info  changed: notes.txt 13 bytes
2026-07-29T13:00:32-03:00 info  stopped
```

`--log-format json` writes one object per line instead, for a collector to pick up:

```json
{"time":"2026-07-29T13:00:30-03:00","level":"info","message":"changed: notes.txt 13 bytes"}
```

A failure that keeps repeating is reported once and then counted, so it cannot bury
everything else. That covers both a script that no longer parses, which fails on every
event, and a path that cannot be watched, which fails on every tick — watching a home
directory with `-r` reaches a permission-denied subdirectory within seconds. Kowl keeps
retrying either way; it just stops narrating.

**Signals.** `SIGINT` and `SIGTERM` shut down cleanly. `SIGHUP` reloads the script.

**Exit codes.** `0` on a clean shutdown or `--help`, `1` when the script cannot be loaded,
`2` on a usage error.

## When something looks wrong

Kowl is quiet when things work, so the lines it does print are worth reading. These are
the ones that come up.

**A hook never fires, and nothing is reported.** The editor probably saved by writing a
new file and renaming it over the old one, which no watch on the original inode can see.
Watch the containing directory instead of the file: `-f ./config` rather than
`-f ./config/app.yml`.

**Nothing fires and the path is on a network or container filesystem.** NFS does not
deliver events at all, and whether they cross a Docker bind mount depends on how files are
shared. Polling does not need events: add `-m 2s`.

**`hooks cannot keep up: dropped N events`.** The hook is slower than the rate of change.
Kowl's queue is bounded on purpose — losing events where you can see it beats blocking the
reader and losing them in the kernel. Make the hook faster, or narrow what is watched.

**`write() is stuck after 30s and was abandoned; it may still be running`.** The hook is
blocked inside a helper — reading a fifo nobody writes to, or a file on a mount that
stopped answering. Kowl gave up waiting and carried on, but that hook is still out there.
`kExec`, `kCli` and `kSleep` have their own limits and never end up here.

**`write() exceeded 30s and was interrupted`.** The hook was busy in JavaScript, most
likely a loop that does not end. Its VM is discarded and the next event starts clean.

**`--max-watches limit of 4096 reached`.** A recursive watch found more directories than
it is allowed to watch, and the message names one that was left out. Narrow it with `-x`
before raising the limit: `-x node_modules -x .git` usually settles it.

**`lost events on /path: fsnotify: queue or buffer overflow, starting over`.** The
kernel's own queue overflowed and events were lost before Kowl saw them. The watcher is
rebuilt and the path announced again with `EXIST`, which is the cue to resynchronise from
whatever your script tracks.

**The same failure over and over, then silence.** It is still happening. A repeated
failure is reported once and then counted, so it cannot bury everything else; `--log-level
debug` shows every occurrence.

**A hook runs twice for one save.** An editor emits several events per save, and
`--debounce` collapses them; a very slow save may outlast the window. Raise it:
`--debounce 500ms`.

**A hook wakes itself.** Writes made through `kExec` leave no record, so Kowl cannot tell
them from someone else's. Use `kStringToFile` and the other helpers where that matters.

Before starting anything, `--check` says whether the script loads and what it defines, and
`--once` runs it against what is there now.

## Command line reference

```
Usage:
  kowl [OPTIONS]

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
      --check           load the script, report what it defines and exit
      --once            run the hooks against what is there now and exit
  -V, --version         print the version and exit

Help Options:
  -h, --help            Show this help message
```

Every timing flag takes a duration: `-m 5s`, `--debounce 50ms`, `--hook-timeout 2m`. A
bare number is rejected rather than assumed to mean some particular unit. Kowl takes no
positional arguments; every path needs its own `-f`.

Two flags are for trying a script rather than running one:

```
kowl -j watch.js --check                  # loads it, says what it defines, exits
kowl -f 'logs/*.log' -j watch.js --once   # runs the hooks against what is there now
```

`--check` needs no `-f`, exits `1` when the script cannot be loaded or defines no hooks,
and is small enough for a pre-commit hook. `--once` does a single pass, so the hooks it
reaches are `ticker` for each matching path and `not_found` for each pattern matching
nothing; it honours `-x` and exits when the last hook has finished.

## Running in a container

```
docker build -t kowl .
docker run --rm -v "$PWD":/watch kowl -f /watch -r -x .git -j /watch/example.js
```

Exclude `.git`, or a recursive watch spends most of its budget on paths nobody wants a
hook for — on this repository, 93 watched paths instead of 4.

The image carries a shell and CA certificates, because a script can call anything through
`kExec` and `kCli`. Whether events cross a bind mount from a macOS or Windows host depends
on how Docker shares files — they do with virtiofs, and historically did not with
gRPC-FUSE. If a change on the host never reaches a hook, that is why, and polling is the
way around it: `-m 2s` notices the change without needing an event. The same applies to
NFS and other network filesystems, which do not deliver events at all.

## Building and testing

```
go install github.com/arturoeanton/Kowl@latest        # into $GOBIN
go build -o kowl .                                     # from a clone
go build -ldflags "-X main.version=1.2.0" -o kowl .    # stamp a release version
```

Dependencies are pinned in `go.mod`. Without a stamped version, `--version` reports the
module version when the binary came from `go install`, or the commit Go records when it
came from a clone, and says so when the tree was dirty.

```
go test ./...                  # everything
go test -race ./...            # the concurrency cover
go test -short ./...           # skips the tests that build the binary
go test -run TestDispatch .    # one group
go test -v -run TestEncrypt ./js
```

The suite includes tests over this file: every `k` helper named here has to exist, every
JavaScript example has to parse, every link has to resolve, and the options block above
has to match what `kowl -h` prints. Documentation that drifts fails the build.

There is no CI, so the Makefile is the bar:

```
make verify          # gofmt, go vet, staticcheck, -race and coverage
make verify-linux    # the same in a container
```

`make verify-linux` matters because fsnotify uses a different backend on each platform:
kqueue on macOS and the BSDs, inotify on Linux. Passing on one says nothing about the
other.

## Notes and limits

* fsnotify uses inotify on Linux, kqueue on macOS and the BSDs, and
  ReadDirectoryChangesW on Windows. The tests assume a Unix shell.
* The JavaScript engine is goja, which is ES2015+ but not a browser or Node: there is no
  DOM, no `require`, no `fetch` and no event loop. Use `kCli` for HTTP and `kExec` for
  anything else.
* Watchers follow inodes, not paths. Kowl rebuilds one when the file it was watching is
  deleted and recreated, which it notices within a second.
* If the kernel's own queue overflows, events are lost before Kowl ever sees them. That is
  reported, and the watcher for the affected path is rebuilt so the path is announced
  again with `EXIST` — a script's cue to resynchronise from whatever it tracks.
* `kExec` runs whatever a script asks it to, with the privileges of the Kowl process.
  Treat the script file as trusted input.
* There is no CI. `go vet`, `gofmt -l` and `go test -race ./...` are the bar.
