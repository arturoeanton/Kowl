# Kowl 
Kowl is one watcher of files that (thank to [fsnotify](https://github.com/fsnotify/fsnotify) ) , it trigger scripts js (thank to [OttoJs](https://github.com/robertkrimen/otto)). 

[![asciicast](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf.svg)](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf)


### Dependencies
go get -u github.com/robertkrimen/otto
go get -u github.com/fsnotify/fsnotify
go get -u gopkg.in/h2non/gentleman.v2
go get -u github.com/jessevdk/go-flags


### Run
Example 1
```
./kowl -f /tmp/foo -j example.js
```
Example 2
```
./kowl -f /tmp/foo -j example.js -w
```


### Help
```
./kowl -h
Usage:
  kowl [OPTIONS]

Application Options:
  -f, --filename=       filename that wants to be observed
  -j, --javascript=     Js that wants that executes the actions
  -m, --millisecond=    Millisecond change check defaul 1000 (default: 1000)
  -w, --flagNotWatcher  Watcher disable

Help Options:
  -h, --help            Show this help message
```

### Script JS
```
function not_found(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())
}

function ticker(name, op, args, t) {
    console.log(name + " - " + op + " - " + args, kNow())
}

function exist(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())
}

function write(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())
}

function create(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())
}

function remove(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())
}

function rename(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())

}

function chmod(name, op, args) {
    console.log(name + " - " + op + " - " + args, kNow())
}
```

### Functions

#### kExec

> func  kExec(name string, arg ...string) (string,string, int)

```
out = kExec("ls","-l")
stdout = out[0]  
stderr = out[1]
err = out[3] 
```

other example:
```
out = kExec("curl","www.google.com")
console.log(out[0])
```


you can use  underscore.js.  
Example:
```
var stooges = [{name: 'moe', age: 40}, {name: 'larry', age: 50}, {name: 'curly', age: 60}];
var data = _.pluck(stooges, 'name');
console.log(data)
```


#### kFileToString
> func kFileToString(filename string) (string, int)  
```
data = kFileToString("line\n","file.txt")
string = data[0]
err = data[1]
```

#### kStringToFile 
> func kStringToFile( value string, filename string ) int   
```
err = kStringToFile("line\n","file.txt")
```

#### kAppendFile
> func kAppendFile(value, filename string) int  
```
err = kAppendFile("string\n","file.txt") 
```

#### kRemoveFile
> func kRemoveFile(filename string) int  
```
err = kRemoveFile("file.txt")

```

#### kEncrypt/kDecrypt
> func KEncrypt(skey , message string) ( string, int)
> func KDecrypt(skey, securemess string) ( string,  int)
```
data = kEncrypt("1234","textoPlano")
data = kDecrypt("1234",data[0])
console.log(data[0])

```

#### Integration with [Gentleman](https://github.com/h2non/gentleman)
> Example GET
```
kCli.URL("http://httpbin.org")
req = kCli.Request()
req.Path("/headers")
req.SetHeader("Client", "gentleman")
res = req.Send()
console.log("Body: "+ res[0].String())
```

> Example POST
```
kCli.URL("http://httpbin.org/post")
req1 = kCli.Request()
req1.Method("POST")
req1.Use(kBodyJSON({"foo": "bar"}))
res1 = req1.Send()


console.log("Status: ", res1[0].StatusCode)
console.log("\nBody: ", res1[0].String())
```

#### Utils SO

> kGetEnv    =  os.Getenv  
> kSetEnv    =  os.Setenv  
> kHostname  =  os.Hostname  
> kGetpid    =  os.Getpid  
> kGetppid   =  os.Getppid  
> kGetgid    =  os.Getgid  
> kGetuid    =  os.Getuid  
> kGetegid   =  os.Getegid  
> kArgs      =  os.Args  

> Example 1
```
kSetEnv("VAR","data")
d = kGetEnv("VAR")
console.log(d)
console.log(kHostname())

```

#### Example for rewrite one value in file
```
function write(name, op, args) {
    if (kGetEnv("kFlag") != 1){
        data = kFileToString(name) ;
        kStringToFile("8080",name)
        kSetEnv("kFlag",1)
    }else{
        kSetEnv("kFlag",0)
    }
}
```


##### Notes: 
* fsnotify used iNotify
