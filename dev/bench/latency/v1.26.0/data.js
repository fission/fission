window.BENCHMARK_DATA = {
  "lastUpdate": 1784538492909,
  "repoUrl": "https://github.com/fission/fission",
  "entries": {
    "Fission latency (v1.26.0)": [
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
        "date": 1784538491993,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 103.016,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 185.406,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 187.656,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 56,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2848.587,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 3477.956,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 9292.566,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 735,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 140.785,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 292.945,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 366.356,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 291,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 3019.337,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 6154.142,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 7110.863,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 522,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 2123.489,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 4481.711,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 5388.552,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 56,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 22.671,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 43.967,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 57.023,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 76.223,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 111.999,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 289,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 24.303,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 46.847,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 61.247,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 83.135,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 135.935,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 79,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 5.899,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 10.527,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 14.263,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 21.375,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 80.319,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 23.279,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 48.479,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 71.807,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 130.175,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 213.887,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 35.775,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 138.495,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 382.975,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 623.615,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 761.343,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 39.999,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 914.431,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 1596.415,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 2353.151,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 2893.823,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 80.575,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 637.439,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 1586.175,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 36175.871,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 58097.663,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.05542154143046677,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 89,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 493,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.203,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 3.371,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 5.575,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 30.207,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 65.727,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 2.035,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 2.727,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 4.175,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 39.263,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 75.455,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.953,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.985,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 5.155,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 9.591,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 23.599,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 2.591,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 7.655,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 112.767,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 436.735,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 656.383,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 314,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 24.783,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 50.175,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 82.367,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 161.535,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 296.703,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 27.855,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 58.527,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 91.071,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 184.575,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 350.975,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 72.639,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 117.631,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 155.391,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 211.071,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 253.183,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 341.247,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 1141.759,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 27262.975,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 54853.631,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 55181.311,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0.047006155567991044,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 417,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.026562109,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 38,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 5.604026473,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 80.2421875,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 47,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 3.171583088,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 255,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 0,
            "unit": "count"
          }
        ]
      }
    ]
  }
}