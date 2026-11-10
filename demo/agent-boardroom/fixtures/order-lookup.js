'use strict';

// order-lookup is the boardroom demo's tool function (see ../specs/
// function-order-lookup.yaml, spec.tool): a trivial, static-data lookup a
// real agent would call mid-conversation. It intentionally does NOT talk to
// support-desk or the state API — it is a plain, stateless function, only
// opted into MCP tool exposure via FunctionSpec.Tool.
//
// Request body: {"orderId": "1001"}. Response: the matching order, or 404
// with {"error": "..."} when the id is unknown.
const ORDERS = {
  1001: { orderId: '1001', item: 'Wireless Mouse', status: 'shipped', eta: '2026-08-27' },
  1002: { orderId: '1002', item: 'Mechanical Keyboard', status: 'processing', eta: '2026-08-30' },
  1003: { orderId: '1003', item: 'USB-C Dock', status: 'delivered', eta: '2026-08-20' },
  1004: { orderId: '1004', item: '4K Monitor', status: 'shipped', eta: '2026-08-28' },
  1005: { orderId: '1005', item: 'Noise-Cancelling Headphones', status: 'processing', eta: '2026-09-02' },
};

module.exports = async function (context) {
  const body = context.request.body;
  const orderId = body && typeof body === 'object' ? String(body.orderId || '') : '';
  const order = ORDERS[orderId];

  if (!order) {
    return {
      status: 404,
      body: JSON.stringify({ error: `unknown orderId ${JSON.stringify(orderId)}` }),
      headers: { 'Content-Type': 'application/json' },
    };
  }

  return {
    status: 200,
    body: JSON.stringify(order),
    headers: { 'Content-Type': 'application/json' },
  };
};
