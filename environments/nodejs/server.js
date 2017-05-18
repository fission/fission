'use strict';

const fs = require('fs');
const path = require('path');
const process = require('process');
const express = require('express');
const app = express();
const bodyParser = require('body-parser');
const morgan = require('morgan');

const {Tracer, BatchRecorder, Annotation} = require('zipkin');
const {HttpLogger} = require('zipkin-transport-http');
const CLSContext = require('zipkin-context-cls');
const zipkinMiddleware = require('zipkin-instrumentation-express').expressMiddleware;

const ctxImpl = new CLSContext(); // if you want to use CLS
const recorder = new BatchRecorder({
    logger: new HttpLogger({
        endpoint: 'http://zipkin.fission:9411/api/v1/spans'
    })
});
const tracer = new Tracer({
    recorder,
    ctxImpl // this would typically be a CLSContext or ExplicitContext
});

const SERVICE_NAME = 'user_func';


// Command line opts
const argv = require('minimist')(process.argv.slice(1));
if (!argv.codepath) {
    argv.codepath = "/userfunc/user";
    console.log("Codepath defaulting to ", argv.codepath);
}
if (!argv.port) {
    console.log("Port defaulting to 8888");
    argv.port = 8888;
}


// Node resolves module paths according to a file's location. We load
// the file from argv.codepath, but tell users to put dependencies in
// the server's package.json; this means the function's dependencies
// are in /usr/src/app/node_modules.  We could be smarter and have the
// function deps in the right place in argv.codepath; but for now we
// just symlink the function's node_modules to the server's
// node_modules.
fs.symlinkSync('/usr/src/app/node_modules', `${path.dirname(argv.codepath)}/node_modules`);

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
        const startTime = process.hrtime();
        userFunction = require(argv.codepath);
        const elapsed = process.hrtime(startTime);
        console.log(`user code loaded in ${elapsed[0]}sec ${elapsed[1]/1000000}ms`);
    } catch(e) {
        console.error(`user code load error: ${e}`);
        res.status(500).send(JSON.stringify(e));
        return;
    }
    res.status(202).send();
}

app.use(zipkinMiddleware({
    tracer,
    serviceName: SERVICE_NAME, // name of this application
}));
// Request logger
app.use(morgan('combined'));

app.use(bodyParser.urlencoded({ extended: false }));
app.use(bodyParser.json());
app.use(bodyParser.raw());

app.post('/specialize', specialize);

// Generic route -- all http requests go to the user function.
app.all('/', function (req, res) {
    if (!userFunction) {
        res.status(500).send("Generic container: no requests supported");
        return;
    }

    const context = {
        request: req,
        response: res,
        tracer: null,
        // TODO: context should also have: URL template params, query string
    };

    let traceId = null;
    tracer.scoped(() => {
        tracer.setId(tracer.createChildId());
        traceId = tracer.id;
        tracer.recordServiceName(SERVICE_NAME);
        tracer.recordAnnotation(new Annotation.ClientSend());
        tracer.recordRpc("user_func::name");
        tracer.recordBinary('fission.user_func', 'some function name');
        tracer.recordBinary('fission.instrumented', 'true');
        context.tracer = tracer;
    });

    function callback(status, body, headers) {
        if (!status)
            return;
        if (headers) {
            for (let name of Object.keys(headers)) {
                res.set(name, headers[name]);
            }
        }
        res.status(status).send(body);
        tracer.scoped(() => {
            tracer.setId(traceId);
            tracer.recordAnnotation(new Annotation.ClientRecv());
        });
    }

    //
    // Customizing the request context
    //
    // If you want to modify the context to add anything to it,
    // you can do that here by adding properties to the context.
    //

    if (userFunction.length === 1) { // One argument (context)
        // Make sure their function returns a promise
        Promise.resolve(userFunction(context)).then(function({ status, body, headers }) {
            callback(status, body, headers);
        }).catch(function(err) {
            console.log(`Function error: ${err}`);
            callback(500, "Internal server error");
        });
    } else { // 2 arguments (context, callback)
        try {
            userFunction(context, callback);
        } catch (err) {
            console.log(`Function error: ${err}`);
            callback(500, "Internal server error");
        }
    }

});

app.listen(argv.port);
