// Every hook is optional: define only the ones you care about. Kowl reports at startup
// which of them it found, and reloads this file when you edit it.
//
// Each hook receives (name, op, args):
//   name  the path the event is about
//   op    the operation, uppercased: WRITE, CREATE, EXIST, TICKER, ...
//   args  the arguments Kowl itself was started with

// The VM is kept between events, so ordinary globals persist.
var writes = 0

function exist(name, op, args) {
    console.log(op, name, kNow())
}

function create(name, op, args) {
    console.log(op, name, kNow())
}

function write(name, op, args) {
    writes = writes + 1
    console.log(op, name, "(" + writes + " so far)", kNow())
}

function remove(name, op, args) {
    console.log(op, name, kNow())
}

function rename(name, op, args) {
    console.log(op, name, kNow())
}

function chmod(name, op, args) {
    console.log(op, name, kNow())
}

function ticker(name, op, args) {
    console.log(op, name, kNow())
}

function not_found(name, op, args) {
    console.log(op, name, kNow())
}
