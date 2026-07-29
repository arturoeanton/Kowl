// Every hook is optional: define only the ones you care about. Kowl reports at startup
// which of them it found, and reloads this file when you edit it.
//
// Each hook receives (name, op, event):
//   name   the path the event is about
//   op     the operation, uppercased: WRITE, CREATE, EXIST, TICKER, ...
//   event  {path, op, name, dir, exists, isDir, size, modTime} for that path

// The VM is kept between events, so ordinary globals persist.
var writes = 0

function exist(name, op, event) {
    console.log(op, name, kNow())
}

function create(name, op, event) {
    console.log(op, name, kNow())
}

function write(name, op, event) {
    writes = writes + 1
    console.log(op, event.name, event.size + " bytes", "(" + writes + " so far)", kNow())
}

function remove(name, op, event) {
    console.log(op, name, kNow())
}

function rename(name, op, event) {
    console.log(op, name, kNow())
}

function chmod(name, op, event) {
    console.log(op, name, kNow())
}

function ticker(name, op, event) {
    console.log(op, name, kNow())
}

function not_found(name, op, event) {
    console.log(op, name, kNow())
}
