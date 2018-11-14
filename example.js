function not_found(name, op, args) {
  
    console.log(name + " - " + op + " - " + args)

}

function exist(name, op, args) {
    data = kFileToString(name) ;
    console.log(data)
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
    data = kFileToString(name) ;
    console.log(data[0].split("\n")[1])
    console.log(name + " - " + op + " - " + args)
}

function chmod(name, op, args) {
    data =  kEncrypt("123", "Hello word! ")
    data =   kDecrypt("123", data[0])
    console.log(data[0])
    
    
    kCli.URL("http://httpbin.org")
    req = kCli.Request()
    req.Path("/headers")
    req.SetHeader("Client", "gentleman")
    res = req.Send()
    console.log("Body: "+ res[0].String())

    kCli.URL("http://httpbin.org/post")
    req = kCli.Request()
    req.Method("POST")
    req.Use(kJSON({"foo": "bar"}))
    res = req.Send()
  

  console.log("Status: ", res[0].StatusCode)
  console.log("\nBody: ", res[0].String())



    data = kFileToString(name) ;
    console.log(data[0].split("\n")[1])
    console.log(name + " - " + op + " - " + args)
}