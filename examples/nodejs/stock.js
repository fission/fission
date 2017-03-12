'use strict';

var http = require('http');

module.exports = async function (context) {
    let body = context.request.body;
    console.log(`body text: ${body['text']}`);

    var symbol = body['text'].split(' ')[1];

    return new Promise(function(resolve, reject) {
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
                    var lastTrade = parsed[0]['l_cur'];
                    return resolve({
                        status: 200,
                        body: `{ "text": "${symbol} last traded at ${lastTrade}" }`
                    });
                } catch (e) {
                    return resolve({
                        status: 200,
                        body: `{ "text": "Error (invalid NYSE symbol?)" }`
                    });
                }
            });
        });
    });

}
