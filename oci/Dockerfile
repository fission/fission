# The base layer must be a runtime image that knows how to load
# code from /userfunc. Reuse the Fission Node.js environment image
# you registered in Step 3 — that way the runtime is identical between
# Path A and Path B.
FROM ghcr.io/fission/node-env-22

# Place your code at /userfunc/. Fission's runtime expects the
# function entry point at /userfunc/<filename> when invoked with a
# functionName, or at /userfunc/user when invoked without one.
COPY hello.js /userfunc/user
