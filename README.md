# Kowl

Kowl watches files and runs JavaScript when they change. Filesystem events come from
[fsnotify](https://github.com/fsnotify/fsnotify); the scripts run on
[otto](https://github.com/robertkrimen/otto), an embedded ES5 interpreter, so there is
no Node.js, no npm and no `require`.

[![asciicast](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf.svg)](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf)

## Build

```
go build -o kowl .
go build -ldflags "-X main.version=1.2.0" -o kowl .   # stamp a release version
```

Dependencies are pinned in `go.mod`; `go build` fetches them. Without a stamped version
`kowl --version` falls back to the commit Go records in the binary.

## Run

```
./kowl -f /tmp/foo -j example.js            # watcher plus one poll per second
./kowl -f /tmp/foo -j example.js -w         # polling only
./kowl -f /tmp/foo -j example.js -m 0       # watcher only
./kowl -f /tmp/foo -j example.js -m 5s      # poll every five seconds
./kowl -f 'logs/*.log' -f /etc/hosts -j example.js
./kowl -f ./config -j example.js            # a directory reports events for its files
./kowl -f ./src -r -j example.js            # and the whole tree below it
```

`-f` takes a file, a directory or a glob and may be repeated. Each matching path gets
its own watcher, and new matches are picked up as they appear. Watching the containing
directory is the reliable way to catch editors that save by writing a new file and
renaming it over the old one.

Watching a directory only reports its direct children, because fsnotify does not
recurse. `-r` enumerates the tree instead and watches every directory in it, including
subdirectories created later. Symlinks are not followed, so a link pointing back up the
tree cannot loop. `--max-watches` caps how many paths are watched at once so a recursive
watch over a large tree cannot exhaust the process's file descriptors; hitting it is
reported once.

`kowl --version` prints the build and exits, without needing `-f` or `-j`.

Kowl runs until it is interrupted, and stops cleanly on Ctrl-C or SIGTERM. It exits `0`
on a clean shutdown or `--help`, `1` when the script cannot be loaded, and `2` on a
usage error.

## Options

```
./kowl -h
Usage:
  kowl [OPTIONS]

Application Options:
  -f, --filename=       file, directory or glob to observe, repeatable
  -j, --javascript=     JavaScript file holding the hooks
  -m, --interval=       poll interval, 0 disables polling (default: 1s)
  -w, --flagNotWatcher  disable the filesystem watcher, leaving only polling
  -r, --recursive       watch every directory below a matched directory
      --max-watches=    how many paths may be watched at once (default: 4096)
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

`-w` together with `-m 0` leaves nothing observing anything, and is rejected.

Every timing flag takes a duration: `-m 5s`, `--debounce 50ms`, `--hook-timeout 2m`. A
bare number is rejected rather than assumed to mean some particular unit.

## Hooks

A script implements only the hooks it cares about. Kowl reports at startup which ones it
found, so a typo shows up immediately, and refuses to start if the script defines none
of them or does not parse.

| Hook | When it runs |
| --- | --- |
| `exist(name, op, event)` | a watched path was found and a watcher was attached to it |
| `create(name, op, event)` | a file was created |
| `write(name, op, event)` | a file was written |
| `remove(name, op, event)` | a file was removed |
| `rename(name, op, event)` | a file was renamed |
| `chmod(name, op, event)` | a file's mode changed |
| `ticker(name, op, event)` | polling found the path, once per `-m` interval |
| `not_found(name, op, event)` | polling found nothing matching the pattern |

Every hook receives the same three arguments: `name` is the path the event is about,
`op` is the operation in uppercase (`WRITE`, `NOT_FOUND`, …), and `event` describes the
path as it stands when the hook runs:

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

On a `REMOVE`, or when a rename beat the hook to it, the path is already gone by the
time the hook runs; `event.exists` is what says whether the rest of the fields mean
anything.

```js
function write(name, op, event) {
    if (event.size > 1024 * 1024) {
        console.log(event.name, "is getting large")
    }
}
```

See `example.js`. `kArgs` still holds the arguments Kowl itself was started with.

Hooks never run concurrently, so a script can keep state in ordinary globals. The VM is
kept between events and reloaded when the script file changes, which means edits take
effect without a restart and reset the globals.

A hook that runs longer than `--hook-timeout` is interrupted and its VM discarded; the
next event starts from a freshly loaded script. The same limit covers the script's top
level, so a loop among the statements outside your functions is reported rather than
left to hang.

### Backpressure

Events are queued and run one at a time, in order. Nothing upstream waits for a hook:
the fsnotify readers and the code that starts and stops watchers keep going while one
runs, so a slow hook cannot stall watch bookkeeping or back the kernel's event queue up
behind it.

The queue is bounded. A hook that never keeps up eventually costs events, and Kowl says
so rather than blocking the readers and letting the kernel drop them where nobody can
see it happen. If you see `hooks cannot keep up`, the hook is too slow for the rate of
change, not the other way round.

### Debouncing and self-triggering

One save from an editor usually produces several write events. Kowl collapses write,
create and chmod events over a quiet period so the hook runs once, after the file has
settled. Use `--debounce 0` to run on every event instead.

A hook that writes the file it was woken for would otherwise wake itself again. Kowl
remembers the state a hook leaves a file in and ignores the next event while the file is
still exactly in that state, so the loop below terminates on its own:

```js
function write(name, op, args) {
    kStringToFile("port=8080", name)
}
```

Pass `--self-trigger` if a script really does want to react to its own writes.

## Functions

Helpers throw a JavaScript exception on failure and return the value on success, so a
hook that ignores an error stops instead of continuing with bad data. Wrap a call in
`try`/`catch` to handle it yourself.

#### kExec

> `kExec(name, ...args) -> {stdout, stderr, code, truncated}`

```js
var out = kExec("ls", "-l")
console.log(out.stdout)
```

A command that runs and exits non-zero is not an error: `code` holds the exit status and
`stdout` and `stderr` hold whatever it produced. Only a command that could not be run at
all, or that outlived `--exec-timeout`, throws. Output beyond `--max-output` is dropped
and `truncated` is set.

```js
var out = kExec("curl", "-s", "https://example.com")
if (out.code !== 0) {
    console.log("curl failed:", out.stderr)
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

`env` is added to the environment Kowl already has rather than replacing it, so a
command never silently loses `PATH`. Arguments are passed to the command directly, with
no shell in between: use `kExec("sh", "-c", ...)` if you want one, and remember what that
means for anything interpolated into the string.

#### kFileToString

> `kFileToString(filename) -> string`

```js
var text = kFileToString("file.txt")
```

#### kStringToFile

> `kStringToFile(value, filename)`

Replaces the contents of the file, creating it if needed.

#### kAppendFile

> `kAppendFile(value, filename)`

Appends to the file, creating it if it does not exist.

#### kRemoveFile

> `kRemoveFile(filename)`

Reports an error when the file is not there. Use `kRemoveAll` for the forgiving version.

#### Inspecting the filesystem

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

#### Changing the filesystem

> `kMkdirAll(path)`
> `kRemoveAll(path)`
> `kCopyFile(source, destination)`
> `kMoveFile(source, destination)`

`kMkdirAll` creates every parent it needs and does not mind an existing directory.
`kRemoveAll` deletes a whole tree and does not mind a path that was never there.
`kCopyFile` preserves permissions and replaces the destination; its directory must
already exist. `kMoveFile` renames, falling back to a copy when the two paths are on
different filesystems.

#### kEncrypt / kDecrypt

> `kEncrypt(passphrase, plaintext) -> string`
> `kDecrypt(passphrase, ciphertext) -> string`

```js
var sealed = kEncrypt("passphrase", "plain text")
console.log(kDecrypt("passphrase", sealed))
```

The passphrase is stretched with PBKDF2-HMAC-SHA256 and the payload is sealed with
AES-256-GCM, so a modified ciphertext is rejected rather than silently decrypted. A
fresh salt and nonce per call mean the same input never produces the same output.
Ciphertexts written by Kowl before this scheme cannot be read.

#### HTTP, via [gentleman](https://github.com/h2non/gentleman)

```js
kCli.URL("http://httpbin.org")
var req = kCli.Request()
req.Path("/headers")
req.SetHeader("Client", "gentleman")
var res = req.Send()
console.log("Body: " + res[0].String())
```

```js
kCli.URL("http://httpbin.org/post")
var req = kCli.Request()
req.Method("POST")
req.Use(kBodyJSON({"foo": "bar"}))
var res = req.Send()
console.log("Status:", res[0].StatusCode)
console.log("Body:", res[0].String())
```

`kBodyJSON`, `kBodyXML` and `kBodyString` build request bodies. Requests time out after
`--http-timeout`. These are the Go objects exposed directly, so `Send()` returns Go's
`(response, error)` pair as a two-element array.

#### Logging

> `kDebug(...)` `kLog(...)` `kWarn(...)` `kError(...)`

`console.log`, `console.info`, `console.debug`, `console.warn` and `console.error` are
wired to the same place. Everything a script logs goes through Kowl's own output:
timestamped, level-prefixed, on stderr, and filtered by `--log-level`. Arguments are
joined with spaces and objects print as their contents.

```js
function write(name, op, event) {
    kDebug("woken for", event.name)
    if (event.size === 0) {
        kWarn(event.name, "is empty")
    }
}
```

#### Operating system

> `kGetEnv` `kSetEnv` `kHostname` `kGetpid` `kGetppid` `kGetgid` `kGetuid` `kGetegid`
> `kArgs` `kNow`

```js
kSetEnv("VAR", "data")
console.log(kGetEnv("VAR"), kHostname(), kNow())
```

#### underscore.js

[underscore](https://underscorejs.org) is available as `_`:

```js
var stooges = [{name: 'moe', age: 40}, {name: 'larry', age: 50}]
console.log(_.pluck(stooges, 'name'))
```

## Output

Everything Kowl and its scripts report goes to stderr, timestamped and leveled:

```
2026-07-29T10:15:04-03:00 info  watching /tmp/foo with example.js (hooks: exist, write)
2026-07-29T10:15:07-03:00 info  WRITE /tmp/foo 12 bytes
```

`--log-format json` writes one object per line instead, for a collector to pick up:

```json
{"time":"2026-07-29T10:15:07-03:00","level":"info","message":"WRITE /tmp/foo 12 bytes"}
```

## Tests

```
go test ./...                  # everything
go test -race ./...            # the concurrency cover
go test -short ./...           # skips the test that builds the binary
go test -run TestDispatch .    # one group
go test -v -run TestEncrypt ./js
```

## Notes

* fsnotify uses inotify on Linux, kqueue on macOS and the BSDs, and ReadDirectoryChangesW
  on Windows.
* Watchers follow inodes, not paths. Kowl rebuilds a watcher when the file it was
  watching is deleted and recreated, which it notices within a second.
* `kExec` runs whatever a script asks it to, with the privileges of the Kowl process.
  Treat the script file as trusted input.
