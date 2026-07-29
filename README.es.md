# Kowl

Observá archivos, ejecutá JavaScript cuando cambian.

Los eventos del sistema de archivos vienen de
[fsnotify](https://github.com/fsnotify/fsnotify); los scripts corren sobre
[otto](https://github.com/robertkrimen/otto), un intérprete ES5 embebido. No hay Node.js,
ni npm, ni `require` — un binario estático y un archivo `.js`.

*[Read this in English](README.md)*

[![asciicast](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf.svg)](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf)

- [Arranque rápido](#arranque-rápido)
- [Qué observar](#qué-observar)
- [Hooks](#hooks)
- [Cómo llegan los eventos a tus hooks](#cómo-llegan-los-eventos-a-tus-hooks)
- [API para scripts](#api-para-scripts)
- [Salida y operación](#salida-y-operación)
- [Referencia de la línea de comandos](#referencia-de-la-línea-de-comandos)
- [Compilar y testear](#compilar-y-testear)
- [Notas y límites](#notas-y-límites)

## Arranque rápido

```
go build -o kowl .
```

Escribí sólo los hooks que te interesan:

```js
// watch.js
function write(name, op, event) {
    console.log("cambió:", event.name, event.size + " bytes")
}
```

Apuntá Kowl a un archivo:

```
$ ./kowl -f /tmp/notas.txt -j watch.js
2026-07-29T10:15:04-03:00 info  watching /tmp/notas.txt with watch.js (hooks: write)
2026-07-29T10:15:09-03:00 info  cambió: notas.txt 12 bytes
```

Kowl corre hasta que lo interrumpas y frena limpio con Ctrl-C o `SIGTERM`.

## Qué observar

`-f` acepta un archivo, un directorio o un glob, y se puede repetir. Cada path que
coincide recibe su propio watcher, y las coincidencias nuevas se toman a medida que
aparecen.

```
./kowl -f /tmp/foo -j watch.js                        # un archivo
./kowl -f 'logs/*.log' -f /etc/hosts -j watch.js      # un glob y un archivo
./kowl -f ./config -j watch.js                        # un directorio y sus archivos
./kowl -f ./src -r -j watch.js                        # y todo el árbol debajo
./kowl -f . -r -x node_modules -x .git -j watch.js    # menos el ruido
```

**Directorios.** Observar un directorio reporta eventos de los archivos que hay adentro.
Esa es la forma confiable de capturar editores que guardan escribiendo un archivo nuevo y
renombrándolo sobre el viejo, algo que ningún watch sobre el inodo original va a ver.

**Recursión.** fsnotify no recursa, así que observar un directorio cubre sólo sus hijos
directos. `-r` enumera el árbol y observa cada directorio, incluidos los subdirectorios
creados después. No sigue symlinks, así que un link que apunta hacia arriba no puede
generar un ciclo.

**Exclusiones.** `-x` saltea paths y se puede repetir. Un patrón sin separador se compara
contra el nombre base, así que `-x node_modules` cubre ese directorio esté donde esté en
el árbol; uno con separador se compara contra el path completo, así que
`-x '/srv/app/tmp/*'` queda específico a ese lugar. Con `-r`, a un directorio excluido no
se desciende en absoluto — la diferencia entre saltear `node_modules` y saltear sólo su
primer nivel.

**Límites.** `--max-watches` topea cuántos paths se observan a la vez, para que un watch
recursivo sobre un árbol grande no pueda agotar los descriptores de archivo del proceso.
La búsqueda corta apenas se alcanza el límite, en vez de enumerar el árbol entero y tirar
casi todo, y alcanzarlo se reporta una vez, nombrando un path que quedó afuera.

**Polling.** En paralelo al watcher, Kowl consulta cada intervalo `-m`, que es lo que
produce `ticker` y `not_found`. `-m 0` desactiva el polling, `-w` desactiva el watcher.
Los dos juntos se rechazan: no quedaría nada observando nada.

## Hooks

Un script implementa sólo los hooks que le importan. Kowl reporta al arrancar cuáles
encontró, así un error de tipeo se ve enseguida, y se niega a arrancar si el script no
define ninguno o no parsea.

| Hook | Cuándo corre |
| --- | --- |
| `exist(name, op, event)` | se encontró un path observado y se le enganchó un watcher |
| `create(name, op, event)` | se creó un archivo |
| `write(name, op, event)` | se escribió un archivo |
| `remove(name, op, event)` | se borró un archivo |
| `rename(name, op, event)` | se renombró un archivo |
| `chmod(name, op, event)` | cambiaron los permisos de un archivo |
| `ticker(name, op, event)` | el polling encontró el path, una vez por intervalo `-m` |
| `not_found(name, op, event)` | el polling no encontró nada que coincida con el patrón |

Todos los hooks reciben los mismos tres argumentos. `name` es el path del evento, `op` es
la operación en mayúsculas (`WRITE`, `NOT_FOUND`, …), y `event` describe el path tal como
está en el momento en que corre el hook:

| Campo | |
| --- | --- |
| `event.path` | el path completo, igual que `name` |
| `event.op` | la operación, igual que `op` |
| `event.name` | el nombre base |
| `event.dir` | el directorio que lo contiene |
| `event.exists` | si el path está ahí en este momento |
| `event.isDir` | si es un directorio |
| `event.size` | tamaño en bytes, `0` cuando ya no está |
| `event.modTime` | timestamp RFC 3339, `""` cuando ya no está |

En un `REMOVE`, o cuando un rename le ganó de mano al hook, el path ya no está para cuando
el hook corre. `event.exists` es lo que dice si el resto de los campos significa algo.

```js
function write(name, op, event) {
    if (event.size > 1024 * 1024) {
        kWarn(event.name, "se está poniendo grande")
    }
}
```

Mirá `example.js` para un ejemplo de cada uno.

### Ciclo de vida

Los hooks nunca corren concurrentemente, así que un script puede guardar estado en
variables globales comunes:

```js
var escrituras = 0
function write(name, op, event) {
    escrituras = escrituras + 1
    console.log(event.name, "escrito", escrituras, "veces")
}
```

La VM se mantiene entre eventos y se recarga cuando cambia el archivo del script, así que
las ediciones toman efecto sin reiniciar — y resetean esas globales.

`SIGHUP` también lo recarga, y reporta qué define la copia nueva. Eso cubre lo que
observar el archivo no puede: un script cuyo comportamiento depende del entorno, o de algo
que leyó al cargarse.

```
kill -HUP $(pgrep kowl)
```

Un hook que corre más que `--hook-timeout` se interrumpe y su VM se descarta; el evento
siguiente arranca desde un script recién cargado. El mismo límite cubre el nivel superior
del script, así que un loop entre las sentencias que están fuera de tus funciones se
reporta en vez de colgar el proceso.

## Cómo llegan los eventos a tus hooks

### Contrapresión

Los eventos se encolan y corren de a uno, en orden. Nada río arriba espera a un hook: los
lectores de fsnotify y el código que arranca y frena watchers siguen andando mientras uno
corre, así que un hook lento no puede frenar la administración de watchers ni acumular la
cola de eventos del kernel detrás suyo.

La cola es acotada. Un hook que nunca alcanza el ritmo termina costando eventos, y Kowl lo
dice, en vez de bloquear a los lectores y dejar que el kernel los descarte donde nadie lo
puede ver. Si ves `hooks cannot keep up`, el hook es demasiado lento para el ritmo de
cambio, no al revés.

### Debounce

Un solo guardado de un editor produce varios eventos de escritura. Kowl colapsa los
eventos de escritura, creación y chmod durante un período de calma para que el hook corra
una vez, después de que el archivo se asentó. `--debounce 0` corre en cada evento.

### Auto-disparo

Un hook que escribe un archivo observado se despertaría a sí mismo, y dos hooks que
escriben los archivos del otro se despertarían para siempre. Kowl registra cada path que
un hook cambió a través de los helpers de más abajo, e ignora los eventos que esos cambios
producen mientras el path siga exactamente en el estado en que el hook lo dejó. Así que
esto termina solo:

```js
function write(name, op, event) {
    kStringToFile("port=8080", event.path)
}
```

Las escrituras hechas con `kExec` no dejan ese registro. Para el archivo por el que se
despertó el hook, Kowl cae en comparar el archivo antes y después, que no puede distinguir
la escritura de un hook de una que hizo otro en el mismo momento. Preferí los helpers
cuando esa distinción importa.

`--self-trigger` desactiva todo esto, para un script que sí quiere reaccionar a sus
propias escrituras.

## API para scripts

Los helpers lanzan una excepción de JavaScript ante un fallo y devuelven el valor cuando
salen bien, así que un hook que ignora un error frena en vez de seguir con datos malos.
Usá `try`/`catch` para manejarlo vos.

### Ejecutar comandos

> `kExec(nombre, ...args, [opciones]) -> {stdout, stderr, code, truncated}`

```js
var out = kExec("ls", "-l")
console.log(out.stdout)
```

Un comando que corre y sale distinto de cero **no** es un error: `code` tiene el estado de
salida, y `stdout` y `stderr` tienen lo que haya producido. Sólo lanza excepción un
comando que no se pudo ejecutar en absoluto, o que sobrevivió a `--exec-timeout`. La
salida que pasa `--max-output` se descarta y se marca `truncated`.

```js
var out = kExec("curl", "-s", "https://example.com")
if (out.code !== 0) {
    kError("curl falló:", out.stderr)
}
```

Un objeto al final son opciones, no otro argumento:

```js
kExec("git", "status", "--short", {
    dir:   "/srv/repo",             // directorio de trabajo
    env:   {LANG: "C"},             // se suma al entorno de Kowl, pisando en conflicto
    stdin: "entrada para el comando"
})
```

`env` se suma al entorno que Kowl ya tiene en vez de reemplazarlo, así que un comando nunca
pierde `PATH` en silencio. Los argumentos van al comando directo, sin shell en el medio:
usá `kExec("sh", "-c", ...)` si querés uno, y acordate de lo que eso implica para
cualquier cosa que interpoles en el string.

### Leer y escribir archivos

> `kFileToString(path) -> string`
> `kStringToFile(valor, path)` — reemplaza el contenido, creando el archivo si hace falta
> `kAppendFile(valor, path)` — agrega al final, creando el archivo si hace falta
> `kRemoveFile(path)` — da error cuando el path no está

### Inspeccionar el filesystem

> `kFileExists(path) -> boolean`
> `kStat(path) -> {path, name, dir, size, mode, modTime, isDir}`
> `kListDir(path) -> [{name, path, size, isDir}, ...]`
> `kGlob(patrón) -> [path, ...]`

`kStat` lanza excepción cuando el path no está, así que `kFileExists` es la forma de
preguntar sin manejar una excepción. `kListDir` viene ordenado por nombre, y `kGlob`
devuelve un array vacío en vez de null cuando no coincide nada.

```js
function write(name, op, event) {
    var entradas = kListDir(event.dir)
    for (var i = 0; i < entradas.length; i++) {
        if (!entradas[i].isDir && entradas[i].size === 0) {
            kWarn(entradas[i].name, "está vacío")
        }
    }
}
```

### Modificar el filesystem

> `kMkdirAll(path)` — crea todos los padres que necesite, y no se queja si ya existe
> `kRemoveAll(path)` — borra un árbol entero, y no se queja si el path nunca existió
> `kCopyFile(origen, destino)` — preserva permisos, reemplaza el destino
> `kMoveFile(origen, destino)` — renombra, copiando cuando tiene que cruzar de filesystem

### Cifrado

> `kEncrypt(frase, textoPlano) -> string`
> `kDecrypt(frase, textoCifrado) -> string`

```js
var sellado = kEncrypt("frase secreta", "texto plano")
console.log(kDecrypt("frase secreta", sellado))
```

La frase se estira con PBKDF2-HMAC-SHA256 y el contenido se sella con AES-256-GCM, así que
un texto cifrado modificado se rechaza en vez de descifrarse en silencio. Una sal y un
nonce nuevos por llamada hacen que la misma entrada nunca produzca la misma salida. La
derivación de clave es lenta a propósito; no llames a esto una vez por evento en un watch
con mucho movimiento.

### HTTP

Vía [gentleman](https://github.com/h2non/gentleman), expuesto como `kCli`:

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

`kBodyJSON`, `kBodyXML` y `kBodyString` arman cuerpos de request. Los requests expiran a
los `--http-timeout`. Estos son objetos de Go expuestos directamente, así que `Send()`
devuelve el par `(response, error)` de Go como un array de dos elementos.

### Logging

> `kDebug(...)` `kLog(...)` `kWarn(...)` `kError(...)`

`console.log`, `console.info`, `console.debug`, `console.warn` y `console.error` van al
mismo lugar. Todo lo que loguea un script pasa por la salida de Kowl — con timestamp, con
nivel, por stderr, filtrado por `--log-level`. Los argumentos se unen con espacios y los
objetos se imprimen con su contenido.

### Esperar

> `kSleep(duración)`

```js
kSleep("250ms")   // un string de duración
kSleep(250)       // o un número de milisegundos
```

La espera cuenta contra `--hook-timeout`, y pedir más se rechaza en vez de acortarlo en
silencio: un hook interrumpido a la mitad es peor que uno que nunca arrancó. Los hooks
están serializados, así que dormir en uno frena a los demás.

### Sistema operativo

> `kGetEnv` `kSetEnv` `kHostname` `kGetpid` `kGetppid` `kGetgid` `kGetuid` `kGetegid`
> `kArgs` `kNow`

```js
kSetEnv("VAR", "dato")
console.log(kGetEnv("VAR"), kHostname(), kNow())
```

### underscore.js

[underscore](https://underscorejs.org) está disponible como `_`:

```js
var stooges = [{name: 'moe', age: 40}, {name: 'larry', age: 50}]
console.log(_.pluck(stooges, 'name'))
```

## Salida y operación

Todo lo que reportan Kowl y sus scripts va a **stderr**, con timestamp y nivel:

```
2026-07-29T10:15:04-03:00 info  watching /tmp/foo with watch.js (hooks: exist, write)
2026-07-29T10:15:07-03:00 info  WRITE /tmp/foo 12 bytes
```

`--log-format json` escribe un objeto por línea, para que lo levante un colector:

```json
{"time":"2026-07-29T10:15:07-03:00","level":"info","message":"WRITE /tmp/foo 12 bytes"}
```

Un fallo que se repite — un script que dejó de parsear, digamos — se reporta una vez y
después se cuenta, así no tapa todo lo demás.

**Señales.** `SIGINT` y `SIGTERM` apagan limpio. `SIGHUP` recarga el script.

**Códigos de salida.** `0` si apagó limpio o fue `--help`, `1` si no se pudo cargar el
script, `2` ante un error de uso.

## Referencia de la línea de comandos

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

Todos los flags de tiempo toman una duración: `-m 5s`, `--debounce 50ms`,
`--hook-timeout 2m`. Un número pelado se rechaza en vez de asumir alguna unidad. Kowl no
toma argumentos posicionales; cada path necesita su propio `-f`.

## Compilar y testear

```
go build -o kowl .
go build -ldflags "-X main.version=1.2.0" -o kowl .   # estampar una versión de release
```

Las dependencias están fijadas en `go.mod`. Sin una versión estampada, `kowl --version`
cae al commit que Go graba en el binario, y avisa si el árbol estaba sucio.

```
go test ./...                  # todo
go test -race ./...            # la cobertura de concurrencia
go test -short ./...           # saltea los tests que compilan el binario
go test -run TestDispatch .    # un grupo
go test -v -run TestEncrypt ./js
```

## Notas y límites

* fsnotify usa inotify en Linux, kqueue en macOS y los BSD, y ReadDirectoryChangesW en
  Windows. Los tests asumen una shell Unix.
* Los watchers siguen inodos, no paths. Kowl reconstruye uno cuando el archivo que estaba
  observando se borra y se vuelve a crear, cosa que nota dentro del segundo.
* Si la cola del propio kernel desborda, los eventos se pierden antes de que Kowl los vea.
  Eso se reporta, y el watcher del path afectado se reconstruye para que el path se
  anuncie de nuevo con `EXIST` — la señal para que un script resincronice contra lo que
  sea que lleva registrado.
* `kExec` ejecuta lo que el script le pida, con los privilegios del proceso Kowl. Tratá el
  archivo del script como entrada de confianza.
* No hay CI. `go vet`, `gofmt -l` y `go test -race ./...` son la vara.
