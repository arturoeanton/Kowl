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




    data = kFileToString(name) 
    console.log(data[0].split("\n")[1])*/
    console.log(name + " - " + op + " - " + args)
}