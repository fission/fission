'use strict';

const http = require('http');
const { Annotation } = require('zipkin');

module.exports = function (context, callback) {
    let traceId = null;
    const {tracer} = context;
    tracer.scoped(() => {
        tracer.setId(tracer.createChildId());
        traceId = tracer.id;
        tracer.recordServiceName("user_self");
        tracer.recordAnnotation(new Annotation.ClientSend());
        tracer.recordRpc("user_func::httpget");
    });
    http.get({
        host: 'controller.fission',
        path: '/',
    }, function(response) {
        let resp = '';
        response.on('data', function(d) {
            resp += d;
        });
        response.on('end', function() {
            tracer.scoped(() => {
                tracer.setId(traceId);
                tracer.recordBinary('user_func.res', resp);
                tracer.recordAnnotation(new Annotation.ClientRecv());
            });
            callback(200, resp);
        });
    });
};