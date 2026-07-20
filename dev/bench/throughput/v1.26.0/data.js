window.BENCHMARK_DATA = {
  "lastUpdate": 1784538495674,
  "repoUrl": "https://github.com/fission/fission",
  "entries": {
    "Fission throughput (v1.26.0)": [
      {
        "commit": {
          "author": {
            "name": "Sai Asish Y",
            "username": "SAY-5",
            "email": "say.apm35@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "755d8e0d9b72f1356e513712a1d39b897e03b90b",
          "message": "fix(executor): guard nil PodSpec and TerminationGracePeriodSeconds in container getDeploymentSpec (#3591)\n\nSigned-off-by: Sai Asish Y <say.apm35@gmail.com>",
          "timestamp": "2026-07-20T08:34:34Z",
          "url": "https://github.com/fission/fission/commit/755d8e0d9b72f1356e513712a1d39b897e03b90b"
        },
        "date": 1784538495002,
        "tool": "customBiggerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/samples",
            "value": 20,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/samples",
            "value": 20,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/samples",
            "value": 20,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/samples",
            "value": 10,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/samples",
            "value": 10,
            "unit": "count"
          },
          {
            "name": "warm-path/throughput",
            "value": 2050.2833333333333,
            "unit": "rps"
          },
          {
            "name": "warm-path/endpointcache_hit_ratio",
            "value": 1,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/throughput",
            "value": 1916.9833333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c10_throughput",
            "value": 1575.65,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c50_throughput",
            "value": 1928.2833333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c100_throughput",
            "value": 1939.8333333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c250_throughput",
            "value": 1942.3166666666666,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c500_throughput",
            "value": 261.05,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps100_throughput",
            "value": 100,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps250_throughput",
            "value": 250,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps500_throughput",
            "value": 500,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps1000_throughput",
            "value": 973.4666666666667,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1KiB_throughput",
            "value": 1802.0833333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/10KiB_throughput",
            "value": 1600.7833333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/100KiB_throughput",
            "value": 669.6,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1MiB_throughput",
            "value": 28.383333333333333,
            "unit": "rps"
          },
          {
            "name": "router-index-scale/objects",
            "value": 1000,
            "unit": "count"
          },
          {
            "name": "route-churn/routes",
            "value": 500,
            "unit": "count"
          }
        ]
      }
    ]
  }
}