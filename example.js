function not_found(name, op, args) {
  
    console.log(name + " - " + op + " - " + args)

}

function exist(name, op, args) {
    data = kFileToString(name) ;
    console.log(data)
    console.log(name + " - " + op + " - " + args)
}

function write(name, op, args) {
 
    if (kGetEnv("kFlag") != 1){
        data = kFileToString(name) ;
        kStringToFile("8080",name)
        kSetEnv("kFlag",1)
    }else{
        kSetEnv("kFlag",0)
    }
    
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
   /* data =  kEncrypt("123", "Hello word! ")
    console.log(data[0])
    data =   kDecrypt("123", data[0])
    console.log(data[0])
    
    
    kCli.URL("http://httpbin.org")
    req = kCli.Request()
    req.Path("/headers")
    req.SetHeader("Client", "gentleman")
    res = req.Send()
    console.log("Body: "+ res[0].String())

    kCli.URL("http://httpbin.org/post")
    req1 = kCli.Request()
    req1.Method("POST")
    req1.Use(kBodyJSON({"foo": "bar"}))
    res1 = req1.Send()
  

  console.log("Status: ", res1[0].StatusCode)
  console.log("\nBody: ", res1[0].String())

    console.log(kHostname())

    data = kFileToString(name) 
    console.log(data[0].split("\n")[1])*/
    console.log(name + " - " + op + " - " + args)
}