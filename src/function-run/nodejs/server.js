'use strict';

var fs = require('fs');
var process = require('process');
var express = require('express');
var app = express();
var bodyParser = require('body-parser');

// Command line opts
const argv = require('minimist')(process.argv.slice(1));
if (!argv.codepath || !argv.port) {
    console.log("Need --codepath and --port");
    process.exit(1);
}

// User function.  Starts out undefined.
let userFunction;

//
// Specialize this server to a given user function.  The user function
// is read from argv.codepath; it's expected to be placed there by the
// fission runtime.
//
function specialize(req, res) {
    // Make sure we're a generic container.  (No reuse of containers.
    // Once specialized, the container remains specialized.)
    if (userFunction) {
        res.status(400).send("Not a generic container");
        return;
    }

    // Read and load the code. It's placed there securely by the fission runtime.
    try {
        var startTime = process.hrtime();

        const code = fs.readFileSync(argv.codepath).toString();
        userFunction = eval(code);

        var elapsed = process.hrtime(startTime);
        console.log(`user code loaded in ${elapsed[0]}sec ${elapsed[1]}ns`);
    } catch(e) {
        console.log(`eval error: ${e}`);
        res.status(500).send(JSON.stringify(e));
        return;
    }
    res.status(202).send();
}


app.use(bodyParser.json());
app.post('/specialize', specialize);

// Generic route -- all http requests go to the user function.
app.all('/', function (req, res) {
    if (!userFunction) {
        res.status(500).send("Generic container: no requests supported");
        return;
    }
    const context = {
        request: req,
        response: res
        // TODO: context should also have: URL template params, query string, ...anything else?
    };
    function callback(status, body, headers) {
        if (!status)
            return;
        if (headers) {
            for (let name of Object.keys(headers)) {
                res.set(name, headers[name]);
            }
        }
        res.status(status).send(body);
    }
    userFunction(context, callback);
});

app.listen(argv.port);
