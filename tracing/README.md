# Tracing

- Traces fission functions as part of a bigger solution
- Helps troubleshoot performance problems with fission

## Deployment

### Fission with router, poolmgr, environment image instrumented

Make sure a instrumented version of fission is deployed.

In router and poolmgr, only the function call code path is instrumented.
Trace are not sampled. Sampling could be switched on in production mode.
Every environment image should be instrumented individually,
currently only nodejs is instrumented.

### Zipkin as tracing backend service

Fission try to use OpenTracing Client API to log the trace, Zipkin supports
the OpenTracing API and is a popular solution with client library, backend service and web ui.

```bash
# Create the Zipkin backend 
# Zipkin service for client to discovery it by DNS
$ kubectl create -f zipkin-deployment.yaml -f zipkin-svc.yaml

# And then port forward Zipkin pod port 9411 to local
# Visit localhost:9411 for ui
```

## Example

`node-tracing-example.js` fetches `http://controller.fission` and return the response to the client.
It uses the `tracer` in context. Other
[instrumented libraries](https://github.com/openzipkin/zipkin-js/tree/master/packages) by Zipkin is useful.