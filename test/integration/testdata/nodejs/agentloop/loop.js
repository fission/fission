// Agent-runtime continue-loop fixture: reads the turn count the dispatcher
// stamps on every request (X-Fission-Agent-Turns, lowercased by Node), echoes
// it back as {"turns": n}, and asks for another continuation
// (X-Fission-Agent-Yield: continue) while n < 4. Once n >= 4 it sets no
// yield header, ending the chain. Used by TestAgentRuntimeYieldContinue to prove
// server-side wake-driven chaining runs without any introspection API.
module.exports = async function (context) {
    const raw = context.request.headers['x-fission-agent-turns'];
    const n = parseInt(raw, 10) || 0;

    const headers = { 'Content-Type': 'application/json' };
    if (n < 4) {
        headers['X-Fission-Agent-Yield'] = 'continue';
    }

    return {
        status: 200,
        headers: headers,
        body: JSON.stringify({ turns: n }),
    };
};
