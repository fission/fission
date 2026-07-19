// Workflow step fixture: echoes a hop counter so chained Task states can
// prove data threads through the run document ({"hops":N} in, {"hops":N+1} out).
module.exports = async function (context) {
    const body = context.request.body || {};
    const hops = typeof body === 'object' && Number.isInteger(body.hops) ? body.hops : 0;
    return {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ hops: hops + 1, msg: 'hello' }),
    };
};
