# Kowl

Observá archivos, ejecutá JavaScript cuando cambian.

Los eventos del sistema de archivos vienen de
[fsnotify](https://github.com/fsnotify/fsnotify); los scripts corren sobre
[goja](https://github.com/dop251/goja), un intérprete ES2015+ embebido. No hay Node.js, ni
npm, ni `require` — un binario estático y un archivo `.js`.

*[Read this in English](README.md)*

- [Arranque rápido](#arranque-rápido)
- [Para qué sirve](#para-qué-sirve)
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
go install github.com/arturoeanton/Kowl@latest
```

Eso deja un binario llamado `Kowl` en `$GOBIN`, con el nombre del repositorio. Renombralo
si preferís escribir `kowl`.

O desde un clon:

```
go build -o kowl .
```

Escribí sólo los hooks que te interesan:

```js
// watch.js
function write(name, op, event) {
    console.log(`cambió: ${event.name} ${event.size} bytes`)
}
```

Apuntá Kowl a un archivo:

```
$ ./kowl -f notas.txt -j watch.js
2026-07-29T13:00:28-03:00 info  watching notas.txt with watch.js (hooks: write)
2026-07-29T13:00:30-03:00 info  cambió: notas.txt 13 bytes
```

Kowl corre hasta que lo interrumpas y frena limpio con Ctrl-C o `SIGTERM`.

## Para qué sirve

El hueco que llena Kowl está entre un one-liner de shell y un demonio que tenés que
escribir. Un `while true; do ...; done` no tiene debounce, ni manejo de errores, ni logs;
un watcher hecho a medida en Go o Node es un proyecto en sí mismo. Kowl es un binario que
dejás al lado de un script.

Todos los ejemplos de abajo fueron ejecutados tal como están escritos.

### Ejecutar algo cuando se guarda un archivo

El caso obvio — compilar, lintear, testear, recargar. `--debounce` colapsa la ráfaga de
eventos que produce un solo guardado, así que esto corre una vez por guardado en lugar de
tres veces sobre un archivo a medio escribir.

```js
function write(name, op, event) {
    if (!event.name.endsWith(".txt")) return
    const out = kExec("wc", "-l", event.path)
    if (out.code !== 0) {
        kError(`falló la verificación: ${out.stderr.trim()}`)
        return
    }
    kLog(`ok: ${out.stdout.trim()}`)
}
```

```
./kowl -f src -r -j build.js -m 0 --debounce 100ms
```

### Procesar archivos que caen en un directorio

El patrón de bandeja de entrada: algo deposita archivos, vos los levantás, hacés algo y
los sacás de ahí. `create` dispara al llegar, y sacar el archivo con `kMoveFile` no vuelve
a despertar al hook.

```js
function create(name, op, event) {
    if (event.isDir || !event.name.endsWith(".csv")) return
    kSleep("100ms")                                   // dejá que termine de escribirse
    const filas = kFileToString(event.path).trim().split("\n").length
    kMoveFile(event.path, `${kGetEnv("DONE")}/${event.name}`)
    kLog(`procesado ${event.name}: ${filas} filas`)
}
```

```
DONE=./done ./kowl -f inbox -j inbox.js -m 0
```

### Detectar algo que dejó de pasar

`ticker` y `not_found` disparan en cada intervalo de polling, haya cambiado algo o no, que
es lo que hace detectable una ausencia. Esto es un *dead man's switch* sobre un archivo de
latido: silencioso mientras todo anda, una línea cuando se pone viejo y otra cuando se
recupera.

```js
const VIEJO_MS = 60000
let alertado = false

function ticker(name, op, event) {
    const edad = Date.now() - new Date(event.modTime).getTime()
    if (edad > VIEJO_MS && !alertado) {
        kError(`el latido tiene ${Math.round(edad / 1000)}s`)
        alertado = true
    } else if (edad <= VIEJO_MS && alertado) {
        kLog("el latido se recuperó")
        alertado = false
    }
}

function not_found(name, op, event) {
    if (!alertado) {
        kError("falta el archivo de latido")
        alertado = true
    }
}
```

```
./kowl -f /var/run/app.beat -j heartbeat.js -m 10s -w
```

### Recargar un servicio cuando cambia su configuración

Validá primero, y señalá al servicio sólo si el archivo nuevo está bien — un watcher que
recarga una configuración rota es peor que no tener watcher.

```js
function write(name, op, event) {
    const check = kExec("nginx", "-t", "-c", event.path)
    if (check.code !== 0) {
        kError(`configuración rechazada, no recargo: ${check.stderr.trim()}`)
        return
    }
    kCopyFile(event.path, `${event.path}.good`)
    kExec("systemctl", "reload", "nginx")
    kLog("recargado")
}
```

### Mandar los cambios a otro lado

`kCli` es un cliente HTTP, así que un cambio puede convertirse en un webhook, una métrica
o un mensaje sin salir a invocar `curl`.

```js
function create(name, op, event) {
    kCli.URL(kGetEnv("WEBHOOK"))
    const req = kCli.Request()
    req.Method("POST")
    req.Use(kBodyJSON({file: event.name, size: event.size, at: event.modTime}))
    const res = req.Send()
    if (res.statusCode >= 300) {
        kWarn(`el webhook devolvió ${res.statusCode}`)
    }
}
```

Kowl no encaja bien cuando necesitás tiempos de reacción de menos de un milisegundo,
cuando el trabajo por evento es lo bastante pesado como para querer concurrencia real, o
cuando ya estás dentro de un runtime que observa archivos por vos. No tiene scheduler, ni
una cola que sobreviva a un reinicio, ni clustering.

## Qué observar

`-f` acepta un archivo, un directorio o un glob, y se puede repetir. Cada path que coincide
recibe su propio watcher, y las coincidencias nuevas se toman a medida que aparecen.

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
contra el nombre base, así que `-x node_modules` cubre ese directorio esté donde esté en el
árbol; uno con separador se compara contra el path completo, así que `-x '/srv/app/tmp/*'`
queda específico a ese lugar. Con `-r`, a un directorio excluido no se desciende en
absoluto — la diferencia entre saltear `node_modules` y saltear sólo su primer nivel.

**Nombres que parecen patrones.** Un patrón se usa primero como glob. Si no matchea nada
y existe un archivo con ese nombre exacto, se observa ese archivo — lo mismo que hace una
shell con un glob sin coincidencias. Eso es lo que hace que `-f 'report[1].pdf'` funcione,
que importa porque así nombra un navegador la segunda descarga. `-x` sigue la misma regla.
Un patrón vacío se rechaza en vez de matchear nada en silencio.

**Límites.** `--max-watches` topea cuántos paths se observan a la vez, para que un watch
recursivo sobre un árbol grande no pueda agotar los descriptores de archivo del proceso. La
búsqueda corta apenas se alcanza el límite, en vez de enumerar el árbol entero y tirar casi
todo, y alcanzarlo se reporta una vez, nombrando un path que quedó afuera.

**Polling.** En paralelo al watcher, Kowl consulta cada intervalo `-m`, que es lo que
produce `ticker` y `not_found`. `-m 0` desactiva el polling, `-w` desactiva el watcher. Los
dos juntos se rechazan: no quedaría nada observando nada.

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

Mirá `example.js` para un ejemplo de cada hook.

### Ciclo de vida

Los hooks nunca corren concurrentemente, así que un script puede guardar estado en
variables globales comunes:

```js
let escrituras = 0
function write(name, op, event) {
    escrituras += 1
    console.log(`${event.name} escrito ${escrituras} veces`)
}
```

La VM se mantiene entre eventos y se recarga cuando cambia el archivo del script, así que
las ediciones toman efecto sin reiniciar — y resetean esas globales.

`SIGHUP` también lo recarga, y reporta qué define la copia nueva. Eso cubre lo que observar
el archivo no puede: un script cuyo comportamiento depende del entorno, o de algo que leyó
al cargarse.

```
kill -HUP $(pgrep kowl)
```

Un hook que corre más que `--hook-timeout` se interrumpe y su VM se descarta; el evento
siguiente arranca desde un script recién cargado. El mismo límite cubre el nivel superior
del script, así que un loop entre las sentencias que están fuera de tus funciones se
reporta en vez de colgar el proceso.

La interrupción sólo actúa entre sentencias de JavaScript, así que no puede alcanzar a un
hook que quedó adentro de uno de los helpers de más abajo — leyendo un fifo que nadie
escribe, o un archivo en un montaje que dejó de responder. Unos segundos después del
timeout, ese hook se **abandona**: Kowl lo dice y vuelve a atender eventos, pero el hook
sigue por ahí y puede terminar más tarde. `kExec`, `kCli` y `kSleep` tienen sus propios
límites y nunca llegan a esto.

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

Un solo guardado de un editor produce varios eventos de escritura. Kowl colapsa los eventos
de escritura, creación y chmod durante un período de calma para que el hook corra una vez,
después de que el archivo se asentó. `--debounce 0` corre en cada evento.

### Auto-disparo

Un hook que escribe un archivo observado se despertaría a sí mismo, y dos hooks que
escriben los archivos del otro se despertarían para siempre. Kowl registra cada path que un
hook cambió a través de los helpers de más abajo, e ignora los eventos que esos cambios
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

`--self-trigger` desactiva todo esto, para un script que sí quiere reaccionar a sus propias
escrituras.

## API para scripts

Los helpers lanzan una excepción de JavaScript ante un fallo y devuelven el valor cuando
salen bien, así que un hook que ignora un error frena en vez de seguir con datos malos. Usá
`try`/`catch` para manejarlo vos.

### Ejecutar comandos

> `kExec(nombre, ...args, [opciones]) -> {stdout, stderr, code, truncated}`

```js
const out = kExec("ls", "-l")
console.log(out.stdout)
```

Un comando que corre y sale distinto de cero **no** es un error: `code` tiene el estado de
salida, y `stdout` y `stderr` tienen lo que haya producido. Sólo lanza excepción un comando
que no se pudo ejecutar en absoluto, o que sobrevivió a `--exec-timeout`. La salida que
pasa `--max-output` se descarta y se marca `truncated`.

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
usá `kExec("sh", "-c", ...)` si querés uno, y acordate de lo que eso implica para cualquier
cosa que interpoles en el string.

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
    for (const entrada of kListDir(event.dir)) {
        if (!entrada.isDir && entrada.size === 0) {
            kWarn(`${entrada.name} está vacío`)
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
const sellado = kEncrypt("frase secreta", "texto plano")
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

`kBodyJSON`, `kBodyXML` y `kBodyString` arman cuerpos de request. Los requests expiran a
los `--http-timeout`, y `Send()` lanza excepción cuando uno falla.

Estos son objetos de Go expuestos directamente. Sus **campos** llegan a JavaScript en lower
camel case — `res.statusCode`, no `res.StatusCode` — mientras que sus **métodos** conservan
sus nombres de Go, así que es `res.String()` y `kCli.URL()`. La misma regla produce los
nombres de campo de `event`, `kStat` y `kExec`.

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
const stooges = [{name: "moe", age: 40}, {name: "larry", age: 50}]
console.log(_.pluck(stooges, "name"))
```

Está vendorizado en `vendorjs/` y embebido en el binario. Casi todo lo que ofrece ya está
en el lenguaje, así que un script nuevo rara vez lo necesita.

## Salida y operación

Todo lo que reportan Kowl y sus scripts va a **stderr**, con timestamp y nivel:

```
2026-07-29T13:00:28-03:00 info  watching notas.txt with watch.js (hooks: write)
2026-07-29T13:00:30-03:00 info  cambió: notas.txt 13 bytes
2026-07-29T13:00:32-03:00 info  stopped
```

`--log-format json` escribe un objeto por línea, para que lo levante un colector:

```json
{"time":"2026-07-29T13:00:30-03:00","level":"info","message":"cambió: notas.txt 13 bytes"}
```

Un fallo que se repite se reporta una vez y después se cuenta, así no tapa todo lo demás.
Eso cubre tanto un script que dejó de parsear, que falla en cada evento, como un path que
no se puede observar, que falla en cada tick — observar un directorio home con `-r` llega
a un subdirectorio sin permisos en segundos. Kowl sigue reintentando igual; sólo deja de
narrarlo.

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
go install github.com/arturoeanton/Kowl@latest        # en $GOBIN
go build -o kowl .                                     # desde un clon
go build -ldflags "-X main.version=1.2.0" -o kowl .    # estampar una versión de release
```

Las dependencias están fijadas en `go.mod`. Sin una versión estampada, `--version` reporta
la versión del módulo cuando el binario vino de `go install`, o el commit que Go graba
cuando vino de un clon, y avisa si el árbol estaba sucio.

```
go test ./...                  # todo
go test -race ./...            # la cobertura de concurrencia
go test -short ./...           # saltea los tests que compilan el binario
go test -run TestDispatch .    # un grupo
go test -v -run TestEncrypt ./js
```

La suite incluye tests sobre este archivo: cada helper `k` que se nombra acá tiene que
existir, cada ejemplo de JavaScript tiene que parsear, cada link tiene que resolver, y el
bloque de opciones de arriba tiene que coincidir con lo que imprime `kowl -h`. La
documentación que se desactualiza rompe el build.

## Notas y límites

* fsnotify usa inotify en Linux, kqueue en macOS y los BSD, y ReadDirectoryChangesW en
  Windows. Los tests asumen una shell Unix.
* El motor de JavaScript es goja, que es ES2015+ pero no es un navegador ni Node: no hay
  DOM, ni `require`, ni `fetch`, ni event loop. Usá `kCli` para HTTP y `kExec` para el
  resto.
* Los watchers siguen inodos, no paths. Kowl reconstruye uno cuando el archivo que estaba
  observando se borra y se vuelve a crear, cosa que nota dentro del segundo.
* Si la cola del propio kernel desborda, los eventos se pierden antes de que Kowl los vea.
  Eso se reporta, y el watcher del path afectado se reconstruye para que el path se anuncie
  de nuevo con `EXIST` — la señal para que un script resincronice contra lo que sea que
  lleva registrado.
* `kExec` ejecuta lo que el script le pida, con los privilegios del proceso Kowl. Tratá el
  archivo del script como entrada de confianza.
* No hay CI. `go vet`, `gofmt -l` y `go test -race ./...` son la vara.
