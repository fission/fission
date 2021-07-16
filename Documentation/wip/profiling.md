# Profiling Fission with Pprof

Fission uses [net/pprof](https://pkg.go.dev/net/http/pprof) for profiling the code across Fission components.
It would be helpful in identifying performance bottlenecks.

To enable profiling, just set `pprof.enabled` to `true` while installing Fission helm chart.

## Pprof data of component pod

Do port forwarding to port 6060 of the pod,

```sh
kubectl port-forward pod/executor-668dfd7c89-2b2ff 6060:6060
```

Run different commands to get or analyze pprof data,

```sh
go tool pprof http://localhost:6060/debug/pprof/flamegraph

go tool pprof http://localhost:6060/debug/pprof/profile\?seconds\=60
```

You can also analyze with binary to get correct references of source,

```sh
# Download binary from pod
kubectl cp fission/executor-668dfd7c89-2b2ff:/fission-bundle fission-bundle

go tool pprof -http ":49816" fission-bundle http://localhost:49513/debug/pprof
```

You can also download pprof data and visualize/analyze with different compatible tools.
