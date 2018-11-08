# Kowl
Kowl is one watcher of files that, it trigger scripts js.  The Scripts can execute command of console  
(curl, ls, cp, mv, a.out, ...)

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




