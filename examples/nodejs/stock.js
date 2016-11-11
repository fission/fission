'use strict';

var http = require('http');

module.exports = function (context, callback) {    
    let body = context.request.body;
    console.log(`body text: ${body['text']}`);

    var symbol = body['text'].split(' ')[1];

    http.get({
        host: 'finance.google.com',
        path: `/finance/info?q=NYSE:${symbol}`
    }, function(response) {
        var resp = '';
        response.on('data', function(d) {
            resp += d;
        });
        response.on('end', function() {

            try {
                var parsed = JSON.parse(resp.slice(3));
                var lastTrade = parsed[0]['l_cur']
                callback(200, `{ "text": "${symbol} last traded at ${lastTrade}" }`);
            } catch (e) {
                callback(200, `{ "text": "Error (invalid NYSE symbol?)" }`);
            }
        });
    });

}
