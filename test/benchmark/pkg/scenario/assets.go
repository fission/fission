// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

// pythonHello is a trivial Python v1 function: a single file whose main() the
// runtime calls by default. Kept inline (it is tiny and below the literal
// archive size limit) so the benchmark has no runtime fixture dependency.
const pythonHello = "def main():\n    return \"Hello, world!\\n\"\n"

// nodeHello is the Node.js counterpart to pythonHello: a single-file v1
// function the runtime loads with no entrypoint. The Node server is
// event-loop-concurrent, so unlike the Python default (single-threaded
// bjoern) one pod genuinely serves many in-flight requests — which is what
// the concurrency/RPS sweeps need to measure Fission rather than the
// runtime's serialization.
const nodeHello = "module.exports = async function(context) {\n" +
	"    return { status: 200, body: \"Hello, world!\\n\" };\n" +
	"}\n"

// pythonCPUBurn does a fixed chunk of CPU work per request so concurrent load
// drives pod CPU up and the HPA scales — without it a no-op function never
// triggers autoscaling.
const pythonCPUBurn = "def main():\n" +
	"    x = 0\n" +
	"    for i in range(2000000):\n" +
	"        x += i\n" +
	"    return str(x)\n"
