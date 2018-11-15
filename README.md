# Kowl
Kowl is one watcher of files that, it trigger scripts js.  The Scripts can execute command of console  
(curl, ls, cp, mv, a.out, ...)

[![asciicast](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf.svg)](https://asciinema.org/a/mju1Elcqn9O3cFVxklPQp55Tf)


### Run
```
./kowl /tmp/foo script.js
```

### Script JS
```
function not_found(name, op, args) {
    console.log(name + " - " + op + " - " + args)
}

function exist(name, op, args) {
    console.log(name + " - " + op + " - " + args)
}

function write(name, op, args) {
    console.log(name + " - " + op + " - " + args)
}

function create(name, op, args) {
    console.log(name + " - " + op + " - " + args)
}

function remove(name, op, args) {
    console.log(name + " - " + op + " - " + args)
}

function rename(name, op, args) {
    console.log(name + " - " + op + " - " + args)
}

function chmod(name, op, args) {
    console.log(name + " - " + op + " - " + args)
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

#### Integration with  gentleman [link to Google!](https://github.com/h2non/gentleman)
> Example GET
```
kCli.URL("http://httpbin.org")
req = kCli.Request()
req.Path("/headers")
req.SetHeader("Client", "gentleman")
res = req.Send()
console.log("Body: "+ res[0].String())
```

Example POST
```
kCli.URL("http://httpbin.org/post")
req1 = kCli.Request()
req1.Method("POST")
req1.Use(kBodyJSON({"foo": "bar"}))
res1 = req1.Send()


console.log("Status: ", res1[0].StatusCode)
console.log("\nBody: ", res1[0].String())
```




