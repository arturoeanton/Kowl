# Kowl

Kowl watches files and runs JavaScript when they change. Filesystem events come from
[fsnotify](https://github.com/fsnotify/fsnotify); the scripts run on
[otto](https://github.com/robertkrimen/otto), an embedded ES5 interpreter, so there is
no Node.js, no npm and no `require`.

[![asciicast](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf.svg)](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf)

## Build

```
go build -o kowl .
```

Dependencies are pinned in `go.mod`; `go build` fetches them.

## Run

```
./kowl -f /tmp/foo -j example.js            # watcher plus one poll per second
./kowl -f /tmp/foo -j example.js -w         # polling only
./kowl -f /tmp/foo -j example.js -m 0       # watcher only
./kowl -f 'logs/*.log' -f /etc/hosts -j example.js
./kowl -f ./config -j example.js            # a directory reports events for its files
```

`-f` takes a file, a directory or a glob and may be repeated. Each matching path gets
its own watcher, and new matches are picked up as they appear. Watching the containing
directory is the reliable way to catch editors that save by writing a new file and
renaming it over the old one.

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
  -m, --millisecond=    poll interval in milliseconds, 0 disables polling
                        (default: 1000)
  -w, --flagNotWatcher  disable the filesystem watcher, leaving only polling
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
      --log-level=      debug, info or error (default: info)

Help Options:
  -h, --help            Show this help message
```

`-w` together with `-m 0` leaves nothing observing anything, and is rejected.

## Hooks

A script implements only the hooks it cares about. Kowl reports at startup which ones it
found, so a typo shows up immediately, and refuses to start if the script defines none
of them or does not parse.

| Hook | When it runs |
| --- | --- |
| `exist(name, op, args)` | a watched path was found and a watcher was attached to it |
| `create(name, op, args)` | a file was created |
| `write(name, op, args)` | a file was written |
| `remove(name, op, args)` | a file was removed |
| `rename(name, op, args)` | a file was renamed |
| `chmod(name, op, args)` | a file's mode changed |
| `ticker(name, op, args)` | polling found the path, once per `-m` interval |
| `not_found(name, op, args)` | polling found nothing matching the pattern |

Every hook receives the same three arguments: `name` is the path the event is about,
`op` is the operation in uppercase (`WRITE`, `NOT_FOUND`, …), and `args` is the argument
list Kowl itself was started with. See `example.js`.

Hooks never run concurrently, so a script can keep state in ordinary globals. The VM is
kept between events and reloaded when the script file changes, which means edits take
effect without a restart and reset the globals.

A hook that runs longer than `--hook-timeout` is interrupted and its VM discarded; the
next event starts from a freshly loaded script.

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
