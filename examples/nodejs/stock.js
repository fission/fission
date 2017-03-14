'use strict';

const rp = require('request-promise-native');

module.exports = async function (context) {
    const body = context.request.body;
    const symbol = body.symbol

    console.log(`Got symbol: ${symbol}`);

    if (!symbol) {
        return {
            status: 400,
            body: {
                text: 'You must provide a stock symbol.'
            }
        };
    }

    try {
        const response = await rp(`http://finance.google.com/finance/info?q=NYSE:${symbol}`);
        const parsed = JSON.parse(response.slice(3));
        const lastTrade = parsed[0]['l_cur'];
        return {
            status: 200,
            body: {
                text: `${symbol} last traded at ${lastTrade}`
            }
        };
    } catch (e) {
        console.error(e);
        return {
            status: 500,
            body: e
        };
    }
}
