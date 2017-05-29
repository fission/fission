# Metrics Collecting and Visualizing

## Metrics

### Metrics collected by cAdviser gathered by kubernetes-nodes job

container_memory_usage_bytes, container_cpu_usage_seconds_total, etc.

### Metrics exported by kube-api-exporter

k8s_pod_labels: 0 or 1 vector labeled by corresponding k8s pods.

### Metrics collected from router

Common labels:
- cold: whether the call is cold or not (create a new container to run the function or not)
- funcname: the function name
- funcuid: the specific version of function
- path: from which router path
- code: the response http code from the function
- method: http method of function call

**fission_http_calls_total**

A counter vector which counts how many fission HTTP calls to the router, labeled by common labels.

**fission_http_callerrors_total**

A counter vector which counts how many fission errors during HTTP call labeled by reason.

**fission_http_call_latency_seconds_summary**

A summary vector observes the latency in seconds of each HTTP call, latency is caused by user function.

**fission_http_call_delay_seconds_summary**

A summary vector observes the delay in seconds of each HTTP call, delay is caused by fission scheduling.

**fission_http_call_response_size_bytes_summary**

A summary vector observes the response size in bytes of each HTTP call.


## Deployment

### Fission with router instrumented

Make sure a instrumented version of fission deployed
and router service port 8080 exposed for metrics scraping.

### Prometheus

Create the prometheus deployment and service (optional for Grafana).

```bash
$ kubectl create -f prometheus-deployment.yaml -f prometheus-svc.yaml
```

Create a deployment and service to export pod labels and extra info to prometheus.

```bash
$ kubectl create -f ./kube-api-exporter-k8s/
```

The exporting tools thanks to [kube-api-exporter](https://github.com/tomwilkie/kube-api-exporter).

Then forward the 9090 port of prometheus pod to localhost
if you want to use prometheus ui.

```bash
$ ./port-forward.sh prometheus 9090
```

Visit localhost:9090/ui and query the metrics by yourself.

### Grafana for visualizing

```bash
$ kubectl create -f grafana-deployment.yaml
$ ./port-forward.sh grafana 3000
```

Visit localhost:3000 and use Grafana ui.

Create a data source with name `Prometheus`, type `Prometheus`,
Url `http://prometheus.fission:9090`, Access `proxy`.

Export fission metrics dashboard from file `Fission-Metrics-v1.json`.