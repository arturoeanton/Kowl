function not_found(name, op, args) {
    var stooges = [{name: 'moe', age: 40}, {name: 'larry', age: 50}, {name: 'curly', age: 60}];
    var data = _.pluck(stooges, 'name');
    console.log(data)
    /*if (kAppendFile("hola","l") <0){
        kStringToFile("hola","l")
    }
    */
    console.log(name + " - " + op + " - " + args)
}

function exist(name, op, args) {
    //kRemoveFile("l")
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