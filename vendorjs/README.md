# Vendored JavaScript

`underscore-min.js` is [Underscore.js](https://underscorejs.org) 1.13.7, MIT licensed,
(c) 2009-2024 Jeremy Ashkenas and contributors. It is embedded into the binary and
evaluated in every VM so scripts can use `_`.

otto shipped underscore as an importable package. goja does not, so the file lives here.
Most of what it offers is now in the language itself: goja supports ES2015, so `map`,
`filter`, `reduce`, arrow functions and spread are available without it.
