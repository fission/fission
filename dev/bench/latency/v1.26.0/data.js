window.BENCHMARK_DATA = {
  "lastUpdate": 1791190950422,
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
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "a106aa583733511db51aad79c4d9ff0ab6af1243",
          "message": "fix(executor): key remaining executor caches by (UID, Generation) (#3609)\n\n#3596 moved the poolmgr pool cache to (UID, Generation) keys because\nResourceVersion also bumps on status-only writes (not just spec\nchanges), and informer lag across components can make an RV-keyed\nlookup miss the entry a concurrent writer just set. This extends the\nsame fix to every other executor-side cache still keyed on RV:\n\n- pkg/executor/fscache/functionServiceCache.go: the byFunction cache\n  (GetByFunction, GetByFunctionUID, Add, _touchByAddress, DeleteEntry,\n  ListOld) migrated CacheKeyUR -> CacheKeyUG.\n- pkg/executor/executortype/poolmgr/gpm.go: the functionEnv memoization\n  cache migrated the same way.\n- pkg/executor/executor.go: dispatchCreateFuncService's newdeploy/\n  container dispatcher dedup key migrated to CacheKeyUGFromMeta, so two\n  concurrent specialization requests that observed different RVs of the\n  same spec still coalesce onto one specialization.\n- pkg/executor/executortype/{container,newdeploy}mgr.go: updated stale\n  comments referencing the old RV-keyed GetByFunction.\n\nAlso included is the adopt-path fix that keying migration exposed:\nadoptSpecializedPods (the post-restart AdoptExistingResources path)\nbuilt its synthetic ObjectMeta from pod labels/annotations without\nGeneration. Under CacheKeyUG that synthetic entry keys as (UID, 0),\nwhich no live Function (Generation >= 1) can ever match, so\nRefreshFuncPods' GetByFunction -> DeleteEntry would deterministically\nmiss every adopted pod and leak stale byFunction/byAddress entries\nwith dead addresses until TTL reap. adoptSpecializedPods now reads\npod.Labels[fv1.FUNCTION_GENERATION] (stamped at specialization time)\nalongside the existing required-field reads and populates Generation;\na missing or unparsable label is treated like any other missing\nrequired field (log and skip adoption) rather than adopting at\nGeneration 0.\n\npkg/crd/key.go is untouched: CacheKeyUR and its constructors stay in\nplace for the router, which still uses them until its own (UID,\nGeneration) migration lands separately.\n\nExtracted from #3599.\n\nCo-authored-by: Claude Fable 5 <noreply@anthropic.com>",
          "timestamp": "2026-07-24T13:46:27Z",
          "url": "https://github.com/fission/fission/commit/a106aa583733511db51aad79c4d9ff0ab6af1243"
        },
        "date": 1785143470980,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 92.868,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 193.859,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 251.886,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 161,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2846.687,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 3513.993,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 7339.945,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 696,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 147.87,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 195.825,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 208.189,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 383,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 2108.987,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 5146.401,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 6093.7,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 1958.436,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 4953.112,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 5952.946,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 459,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 22.271,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 43.647,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 57.439,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 80.895,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 136.063,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 219,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 23.295,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 45.663,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 59.199,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 82.623,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 112.639,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 101,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 5.843,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 10.175,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 13.335,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 18.719,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 60.927,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 23.343,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 47.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 71.615,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 139.519,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 335.871,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 34.975,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 104.703,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 444.159,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 712.703,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 1071.103,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 36.319,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 1140.735,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 1938.431,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 2584.575,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 2850.815,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 113.279,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 1151.999,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 2672.639,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 41091.071,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 57802.751,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.026146491176296995,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 67,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 402,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.039,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 2.889,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 4.375,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 21.391,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 71.423,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 1.953,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 2.599,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 3.763,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 40.831,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 81.343,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.853,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.881,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 4.947,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 86.719,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 141.439,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 2.391,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 7.067,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 18.463,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 82.367,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 159.615,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 272,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 24.591,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 49.471,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 78.399,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 148.735,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 271.359,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 27.711,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 57.631,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 86.271,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 150.911,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 242.303,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 72.191,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 115.839,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 148.223,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 198.143,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 241.151,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 886.271,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 886.271,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 886.271,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 886.271,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 886.271,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0.9905660377358491,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 259,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.034138721,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 5.469861285,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 81.79296875,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 69,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 3.628409269,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 759,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 1504,
            "unit": "count"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "87a49300c294976e9275bc288d10c33b372ffdbf",
          "message": "timer: phase-anchor @every schedules to the trigger, not to process start (#2660) (#3698)\n\nrobfig/cron's ConstantDelaySchedule — what \"@every 6h\" parses to — computes\nNext(t) as t + delay, and newCron starts the cron when the trigger is registered.\nSo every @every trigger's phase is set by whenever the timer process last started,\nwhich has two consequences.\n\nThe reported one: every @every trigger registered at process start fires at\nstart+d, +2d, …, so a restart collapses independently-authored schedules into a\nsingle thundering herd. The issue states the expectation well — \"the schedule is\nrelative to when the function was deployed\" — and that is exactly what was not\ntrue.\n\nThe unreported one is worse. The phase resets on EVERY restart, so a timer pod\nthat restarts more often than the interval starves the trigger completely: an\n\"@every 24h\" trigger under a daily rollout never fires at all, and node churn can\ndo the same to \"@every 6h\". Nothing carried phase across a restart.\n\nAnchoring to the trigger's own CreationTimestamp fixes both. Firing times become a\nproperty of the trigger — the smallest creation + k*interval after now — so they\nsurvive restarts and, because creation times differ, spread out on their own.\nTestAnchoredScheduleSurvivesRestart asserts the STOCK schedule fires zero times\nacross a restart cycle that outpaces the interval, so the bug is pinned against\nthe real library rather than only the fix being asserted.\n\nThis is the issue's own option 2 and needs no API change. Its option 1, a\nconfigurable offset field, would need a CRD change and would still leave the\nstarvation, since the phase would keep resetting.\n\nOnly fixed-interval schedules are wrapped. Standard cron expressions and the\n@daily/@hourly descriptors are already wall-clock anchored, so wrapping them would\nchange what they mean. A trigger with no CreationTimestamp — a hand-built object\nthat never round-tripped through the API server — keeps the stock schedule, as\nthere is nothing stable to anchor to.\n\nTwo edge cases the arithmetic has to exclude. k is at least 1 even when the timer\nasks about a time before the anchor: CreationTimestamp is server-set, so a pod\nwhose clock trails the API server sees a trigger created in its own future, and\nfiring at the anchor itself would fire it seconds after creation rather than an\ninterval later. And time.Sub saturates at ~292 years rather than wrapping, so a\nfar-past anchor would otherwise overflow the multiply into a time in the PAST,\nwhich cron fires immediately and then recomputes forever.\n\nRegistration now parses the spec itself rather than discarding AddFunc's error,\nand that error propagates through addUpdate. This matters more than it looks:\nthe reconciler stamps Scheduled/Ready=True on whatever addUpdate leaves behind, so\nan error absorbed here would report a trigger as firing on schedule while it has\nno cron entry at all. It now reports Scheduled=False/InvalidCron, the same way a\nspec rejected up front does. There is no TimeTrigger admission webhook — the\nreconciler's IsValidCronSpec is the gate, and this branch is belt and braces\nagainst that parser and this one drifting apart.",
          "timestamp": "2026-08-24T07:40:03Z",
          "url": "https://github.com/fission/fission/commit/87a49300c294976e9275bc288d10c33b372ffdbf"
        },
        "date": 1787561866300,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 78.767,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 196.458,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 217.459,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 104,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2840.67,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 3133.845,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 6803.892,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 703,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 122.459,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 290.925,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 322.488,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 412,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 2662.052,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 5125.508,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 6106.466,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 3471.863,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 6431.095,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 6545.571,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 483,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 14.847,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 30.479,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 39.775,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 54.335,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 85.247,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 227,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 16.495,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 36.447,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 50.495,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 71.231,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 117.055,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 98,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 3.999,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 7.363,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 10.135,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 15.247,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 65.727,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 15.599,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 33.471,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 50.367,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 87.871,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 147.839,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 26.015,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 91.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 204.031,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 320.511,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 551.423,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 81.919,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 402.431,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 1090.559,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 29212.671,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 57901.055,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0.0050870147255689425,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 164.863,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 738.815,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 2082.815,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 30261.247,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 59146.239,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.017012288786482335,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 107,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 469,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.093,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 3.977,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 7.999,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 35.807,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 76.927,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 3.177,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 5631.999,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 7610.367,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 9535.487,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 39550.975,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0.8242897536017234,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.596,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.537,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 5.431,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 30.207,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 99.775,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 1.327,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 3.671,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 16.215,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 71.871,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 166.911,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 16.847,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 36.383,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 56.287,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 98.431,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 155.135,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 19.327,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 44.735,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 71.807,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 137.983,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 247.295,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 46.879,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 95.807,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 207.743,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 5128.191,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 38109.183,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 340.735,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 709.119,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 1087.487,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 22282.239,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 26820.607,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 35.036377482,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 238,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.030418006,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 33,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 4.375261688,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 76.0625,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 28,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 2.418990765,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 1023,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 1525,
            "unit": "count"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "89dc34df8852b5e15ae116bdb132e9e7e3f3af63",
          "message": "hack/run-tlc: bump the tla2tools pin for the 2026-09-10 rebuild (#3723)\n\nThe tlaplus project re-uploaded the v1.8.0 tla2tools.jar release asset on\n2026-09-10 (manifest Implementation-Version \"2.0 2026-09-10\", X-Git-Revision\nc3af5e2dcc6e54860e96b8e7adf7509d3f685d25), so the pinned SHA256 no longer\nmatches and the tlc job fails at the checksum step on every PR. Verified the\nnew jar per the note in the script (Main-class tlc2.TLC, Implementation-Vendor\nMicrosoft Corp., tlc2/TLC.class present, size matches the release asset) and\nbumped the pin.",
          "timestamp": "2026-09-11T16:37:35Z",
          "url": "https://github.com/fission/fission/commit/89dc34df8852b5e15ae116bdb132e9e7e3f3af63"
        },
        "date": 1789377774098,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 82.043,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 269.457,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 600.832,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 292,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2834.622,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 3843.264,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 5142.094,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 633,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 100.706,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 251.503,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 290.455,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 509,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 2411.935,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 4487.133,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 5451.595,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 85,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 2683.468,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 5123.944,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 7095.624,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 10.631,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 24.287,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 32.655,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 47.647,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 93.055,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 399,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 11.919,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 29.535,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 42.463,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 61.279,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 96.191,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 75,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 2.897,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 5.843,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 8.311,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 13.383,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 79.231,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 11.063,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 26.415,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 36.159,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 56.351,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 107.583,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 24.975,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 77.183,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 109.119,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 147.839,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 239.871,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 39.967,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 273.663,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 587.263,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 14024.703,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 59539.455,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0.00340040730153392,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 236.799,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 573.439,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 763.391,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 992.767,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 1221.631,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 70,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 414,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 1.623,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 2.425,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 3.587,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 8.295,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 37.951,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 5.455,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 402.431,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 1359.871,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 49610.751,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 59604.991,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0.0006050420168067227,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.443,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.473,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 5.831,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 50.431,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 101.951,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 1.493,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 3.973,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 14.407,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 59.807,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 154.879,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 413,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 16.831,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 42.079,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 58.047,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 85.439,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 148.095,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 21.999,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 110.143,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 243.839,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 14188.543,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 53477.375,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 50.367,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 105.407,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 149.631,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 288.767,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 450.559,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 275.455,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 403.711,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 541.183,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 675.327,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 791.551,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 400,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 35.101908371,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 206,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.02380187,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 49,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 4.540427984,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 83.3828125,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 16,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 2.512167777,
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
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "254f4435e41d7bd244a62d53bdc099e995fdbe37",
          "message": "Carve feature-independent changes out of the agent runtime (#3710): env runtimeClassName, statesvc EventLog routes, MCP _meta tracing, environment egress NetworkPolicy (#3722)\n\n* chart: per-environment egress NetworkPolicy knob\n\nAdds environmentNetworkPolicy.{enabled,allowClusterDNS,extraEgress}\n(default off) templating an Egress-only NetworkPolicy that selects every\npod carrying the environmentName label (poolmgr + newdeploy env pods).\nDefault posture copies Agent Sandbox's SandboxTemplate default: allow\ngeneral internet egress, deny RFC1918 + link-local (which subsumes the\ncloud metadata IP), cluster DNS allowed unless disabled. No Go changes;\nper-agent egress stays out of scope (CNI-specific).\n\n* chart: document control-plane carve-outs and pin egress netpol namespace scope\n\nThe RFC1918 deny-list also blocks in-cluster control-plane traffic that\ncrosses the same pod/Service CIDRs: fetcher->storagesvc (package fetch,\ncold start) and function->statesvc.\nName those flows in the values.yaml/template comments as what an operator\nneeds to extraEgress-allowlist before enabling; the knob's default posture\nis unchanged (opt-in, off by default, per the Agent Sandbox reference\nposture).\n\nAlso add the namespace-scope render assertion the template's own comment\npromises: the policy must follow fission-function-ns (functionNamespace\nif set, else defaultNamespace), not the release namespace, or it silently\nno-ops.\n\n* chart: render environment-egress NetworkPolicy per function namespace\n\nA single copy in fission-function-ns silently left egress unenforced\nfor functions in additionalFissionNamespaces: NetworkPolicy podSelector\nonly matches pods in its own namespace, and pkg/utils\nNamespaceResolver.GetFunctionNS maps only the DEFAULT namespace through\nfunctionNamespace — every other resource namespace maps to itself.\n\nRange over the same static-tenancy namespace set router/role-dataplane.yaml\nalready uses (functionNamespace/defaultNamespace plus each\nadditionalFissionNamespaces entry) and render one policy per namespace.\nDocument in both the template and values.yaml that dynamic/cluster-tenancy\nnamespaces onboarded at runtime are still outside this knob's coverage,\nsince they're unknown at chart render time.\n\n* test(helm): tighten hasDNSRule to require the kube-system scope\n\nThe old check counted any egress rule mentioning port 53, regardless of\npeer — so a regression dropping the template's load-bearing kube-system\nnamespaceSelector (letting env pods egress to any self-labelled\nk8s-app=kube-dns pod in any namespace) still passed every test. Require\nthe namespaceSelector/podSelector pairing plus both UDP and TCP port 53\nin the same rule before counting it as the cluster-DNS rule.\n\n* chart: document dual-stack and NodeLocal DNSCache gaps in egress knob\n\nThe default posture's internet-allow rule is an IPv4-only 0.0.0.0/0\nipBlock: fail-closed on dual-stack clusters (no accidental IPv6 egress\nopens up) but IPv6 destinations stay unreachable regardless. Separately,\nNodeLocal DNSCache's well-known listener (169.254.20.10) falls inside\nthe link-local deny range, so allowClusterDNS=true's kube-system/\nkube-dns rule does not cover it and DNS breaks on clusters that run it.\nNeither changes the default posture; both note the extraEgress override\nan operator needs.\n\n* chart: extract fission.functionNamespaces helper\n\nrouter/role-dataplane.yaml and executor/networkpolicy-environment-egress.yaml\neach recomputed the same deduped function-namespace list (functionNamespace,\nfalling back to defaultNamespace, plus additionalFissionNamespaces) inline.\nFactor it into a named template, following the space-joined-list precedent\nfission.adoptSecretNamespaces already sets, and switch both call sites to it.\nRendered output is unchanged (verified with a before/after helm template diff).\n\n* charts: deny RFC6598/CGNAT (100.64.0.0/10) in environment egress policy\n\nenvironment-egress's ipBlock except-list covered RFC1918 and link-local\nbut omitted 100.64.0.0/10 (RFC 6598 Carrier-Grade NAT) -- EKS clusters\nrunning VPC CNI custom networking use this range for pod/Service CIDRs,\nso the deny-in-cluster posture silently failed to cover in-cluster\ntraffic on those clusters. Add it to the except-list, document it in\nvalues.yaml alongside the existing IPv4-only/NodeLocal DNSCache caveats,\nand update the helm test's expected except-list.\n\n* test(helm): reuse findAllNamed in findInNamespace\n\n* feat(env): opt-in RuntimeClassName for syscall-isolated pods (runtime + builder)\n\nAdds EnvironmentSpec.RuntimeClassName so an environment can opt its pods\ninto a syscall-isolating RuntimeClass (gVisor/Kata) without hand-building\na full Runtime.PodSpec override. RuntimeClassName already propagated via\nMergePodSpec, but was undiscoverable without this typed field.\n\nFill-if-nil via a shared pkg/executor/util.ApplyEnvRuntimeClass helper,\napplied unconditionally (outside the Runtime.PodSpec/Builder.PodSpec nil\nguards) at the three env-aware pod-spec sites: poolmgr genDeploymentSpec,\nnewdeploy getDeploymentSpec, and buildermgr's builder deployment. A\nRuntime.PodSpec/Builder.PodSpec-set RuntimeClassName always wins, since\nMergePodSpec runs first.\n\nValidation is struct-level CEL on EnvironmentSpec (DNS1123-label charset,\nmax 253 chars), mirrored in Go's EnvironmentSpec.Validate() so the CLI's\npre-flight check agrees with what the apiserver admits. Not applied to\ncontainer-executor functions (no env pod spec to apply it to).\n\n* feat(cli): fission env --runtime-class flag\n\n* feat(cli): accept --runtime-class as alias for --runtimeclass\n\n* test,docs(env): runtime-class reaches the pod template; usage docs\n\n* test(env): >253-char runtimeclass reject case; ungate CEL backstop from NODE_RUNTIME_IMAGE\n\n* test(integration): update RuntimeClassName CEL assertion to the reworded message\n\nThe apiserver CEL message changed in the apis commit (08ba29b1) that fixed its\nself-contradictory 'DNS1123 label ... max 253' wording; update\nTestEnvRuntimeClassCELRoundTrip's expected substring to match. (Missed integration\ntest — the Go-side validator test asserts only wantErr, so it did not catch this.)\n\n* apis,cli(env): reword the runtimeClassName validation message; leave an empty --runtime-class unset\n\n- The RuntimeClassName error message said 'valid DNS1123 label ... max 253\n  chars', which is self-contradictory because a DNS1123 label caps at 63.\n  Reword the Go validator and the CEL marker to describe the actual rule\n  (label charset, up to 253 chars) and note that dots are intentionally\n  excluded because the targeted RuntimeClasses are labels.\n- env create set RuntimeClassName to &\"\" on an explicit --runtime-class=\"\",\n  which server-side validation then rejected; leave the field unset instead,\n  matching env update's clear semantics.\n\n* feat(mcp): _meta trace-context extraction and execute_tool spans (MCP 2026-07-28)\n\n* test(mcp): wait for the tools/call server span to end before asserting\n\nCallTool returning means the CLIENT has the result; the server-side\notelhttp span ends only after the handler finishes writing the response\nstream, so an immediate rec.Ended() read raced that end under CI load\nand found only the initialize POST's span. Re-read via EventuallyWithT\nuntil the execute_tool span's parent lands.\n\n* feat(statesvc): scoped EventLog append/read/head routes for function pods\n\n* statesvc: bound the eventlog append body cap; reject '.' stream segments\n\nTwo review-battery findings:\n- The /v1/eventlog/append body cap is derived from the keyspace's MaxValueBytes\n  (MaxAppendEvents * MaxValueBytes * 4/3, ~85x amplification), but MaxValueBytes\n  was validated only as >= 0 and statesvc sets no other body limit — so a tenant\n  could force the shared statesvc to size an unbounded body, and at extreme\n  values the int64 multiplication overflowed. Bound MaxValueBytes at admission\n  (fv1.MaxStateMaxValueBytes = 4MiB, 16x the default) AND clamp the resolved cap\n  in the handler before deriving the body cap, so it stays bounded and overflow-\n  safe even for a keyspace stored before the bound existed.\n- stateapi.ValidStream rejected a '..' segment but not a bare '.' segment, so\n  'a/./b' aliased to the same internal stream as 'a/b'. Reject '.' too.\n\n* comments: make the carved-out code self-describing\n\nDrop cross-references to code and planning notes that do not exist on\nmain (the agent-runtime dispatcher, its tracing tests, and plan section\nids) from the chart, MCP tracing, statesvc, and env runtime-class test\ncomments. No behavior change.\n\n* statesvc: bound EventInput.Type; cover the eventlog auth matrix\n\n- Cap EventInput.Type at 1-128 bytes. The append-body envelope otherwise\n  left it bounded only by the whole-request LimitReader, so a caller could\n  spend the payload-sized budget on type-string bytes instead of the\n  per-event payload cap.\n- Add TestHandlerEventLogAuth, mirroring TestHandlerScopeForgery's KV-route\n  matrix on the eventlog routes (malformed scheme -> 401, unclaimed\n  keyspace -> 403), and a ValidStream case pinning that embedded dots\n  ('a..b') stay valid.\n- Make the postEventLog test helper generic so each call site's concrete\n  request type stays visible instead of being erased to any.\n\n* poolmgr: fold RuntimeClassName into the environment runtime hash\n\nRuntimeClassName lands in the generic pool template, but envRuntimeHash\ndid not cover it, so changing only that field left existing pool RSes and\nspecialized pods stamped with the old hash considered current: they kept\nserving under the previous RuntimeClass. Hash the effective value with\nomitempty so every environment that does not set the field keeps a\nbyte-identical hash (no pool roll on upgrade; the golden test still\npasses), while setting or switching it recycles warm pods.\n\n* statesvc: byte budgets for eventlog append/read; align admin, embedded, and CRD caps with the value ceiling\n\nThe per-event MaxValueBytes quota and the count caps (64 per append, 500\nper read page) did not bound bytes: 64 events at a 4MiB quota is 256MiB\nper append, and a 500-event page of 4MiB events materializes ~2GiB in the\nstore, the out slice, and the encoder of the shared statesvc.\n\n- stateapi.MaxAppendPayloadBytes (= the value ceiling) bounds the summed\n  payloads of one append; the handler answers 413 request_too_large for a\n  batch over it, and the reader is now an http.MaxBytesReader at the\n  derived wire cap so an oversized body is also a 413 rather than a\n  truncated-JSON 400.\n- stateapi.MaxReadPayloadBytes bounds a read page: the handler clamps the\n  page count to budget / <keyspace per-event cap> BEFORE asking the store,\n  so the bound holds in the store, not only on the wire. Sized so a\n  keyspace at the default cap keeps its full default page (128 >= 100).\n- The admin HMAC verifier buffered at most 512KiB, rejecting a valid\n  append or KV write before the handler ran; it now shares the handler's\n  wire cap (stateapi.MaxRequestBodyBytes).\n- The embedded statestore transport (httpapi.MaxRequestBytes) capped\n  requests at 4MiB, below a base64-inflated value at the 4MiB ceiling; it\n  is now derived from the ceiling and a test pins that it covers\n  statesvc's own cap, so embedded mode never rejects an admitted request.\n- The 4MiB ceiling was enforced only by StateConfig.Validate(), which the\n  admission webhook does not call for CEL-covered fields; add the matching\n  Maximum marker so kubectl/GitOps writers hit the same bound. Function and\n  FunctionVersion CRDs regenerated.\n\n* statestore: BoundedEventLog byte-bounded reads; statesvc reads pages by stored bytes\n\nA read page count derived from the keyspace's CURRENT MaxValueBytes cannot\nbound bytes: the quota is mutable and lowering it does not rewrite events\nappended under a larger one, so a stream of 4MiB events read after the\nquota dropped to 256KiB still asked the store for 128 of them (~512MiB).\n\n- Add the optional statestore.BoundedEventLog interface: ReadBounded is\n  Read under a payload-byte budget, enforced while scanning so the bound\n  holds in the store. At least one event is always returned so paging\n  never stalls; maxBytes <= 0 is plain Read. Implemented natively by the\n  memory driver, sqlstore (sqlite/postgres; the cursor closes at the\n  budget, so at most one event past it is scanned), the embedded HTTP\n  client/server (budget rides the request), and the metered wrapper;\n  statestore.ReadBounded falls back to read-then-trim for a driver that\n  lacks it. Conformance suite covers the contract for every driver.\n- statesvc eventRead now asks the store for MaxReadPayloadBytes of actual\n  stored payload instead of clamping the count by quota; the hard count cap\n  and default page are unchanged. Test lowers the quota after writing\n  ceiling-sized events and asserts the page ends at the budget.\n- The read/head request bodies are read through http.MaxBytesReader (shared\n  readJSONBody helper with append), so a valid document padded past the\n  cap is a 413 request_too_large instead of a silently truncated prefix\n  that still parses.\n\n* poolmgr: hash the effective RuntimeClass, not the dedicated field\n\nutil.ApplyEnvRuntimeClass is fill-if-nil: a Runtime.PodSpec override that\nnames a RuntimeClass wins over spec.runtimeClassName. Hashing the dedicated\nfield unconditionally therefore recycled every warm pool when only the\nshadowed field changed, although the pod template was unchanged. Hash the\nsame effective value the template gets (override, else dedicated field,\nelse empty); the unset case stays byte-identical.\n\n* statesvc: clamp legacy MaxValueBytes once in the index; regen swagger doc; drop a redundant conversion\n\n- FunctionIndex.Upsert clamps a stored MaxValueBytes to the admission\n  ceiling, so the KV PUT/CAS routes and the EventLog routes (and the body\n  caps derived from the quota) enforce one bound on both the bearer and the\n  admin path; the EventLog-only clamp in the handler goes away. A Function\n  stored before the ceiling existed can no longer admit a value the admin\n  HMAC path would reject.\n- Regenerate the swagger type doc for the MaxValueBytes comment change.\n- Remove the int64 conversions golangci-lint (unconvert) flagged in the\n  embedded transport cap test.\n\n* docs(rfc,specs): sync the EventLog and statesvc contracts with the shipped code\n\nNo protocol modeled by the TLA+ specs changes in this PR: the CAS-append\n(workflowfold), the version-CAS cursor subscription (eventlogsub), and\nthe MaxKeys counter race (quota) are untouched, and every direct EventLog\nconsumer stops on an empty page, so a byte-bounded page is a batching\ndetail outside the models. Record that honestly where the specs' scope is\ndocumented, and bring the RFC listings up to date:\n\n- eventlogsub.tla header and specs/README: read-page shape (a consumer's\n  Read batch, the optional BoundedEventLog page, the statesvc read route)\n  is not modeled; the progress guarantee (>= 1 event per non-empty page,\n  stop on empty never on short) is functional and lives in the driver\n  conformance suite. Per-request byte limits have no shared counter, so\n  quota.tla does not model them.\n- RFC-0021: note the shipped EventLog extensions (Head, BoundedEventLog).\n- RFC-0023: add the statesvc eventlog routes with their limits and the\n  MaxValueBytes admission ceiling / legacy clamp.\n\nAll green and negative TLC configs pass with the edited spec.",
          "timestamp": "2026-09-15T15:08:38Z",
          "url": "https://github.com/fission/fission/commit/254f4435e41d7bd244a62d53bdc099e995fdbe37"
        },
        "date": 1791190949260,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 57.599,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 230.235,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 901.27,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 169,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2834.961,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 2859.629,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 4376.501,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 705,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 93.559,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 180.421,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 1172.825,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 355,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 1809.796,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 4741.347,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 5721.564,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 177,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 1974.803,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 5047.705,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 5065.212,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 11.711,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 24.383,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 32.543,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 48.127,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 101.119,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 535,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 13.423,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 31.103,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 43.647,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 59.743,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 88.703,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 74,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 3.317,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 6.215,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 8.719,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 14.143,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 71.103,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 12.591,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 27.039,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 40.319,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 79.295,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 146.559,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 19.535,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 68.543,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 202.495,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 356.863,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 562.175,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 160.767,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 627.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 972.287,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 19349.503,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 58884.095,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0.0007833920877399138,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 361.471,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 1143.807,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 1829.887,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 31883.263,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 58392.575,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.01572599618613636,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 171,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 665,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.147,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 5.955,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 43.967,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 107.967,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 152.063,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 6164.479,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 17072.127,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 17334.271,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 17547.263,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 48758.783,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0.7858768406961179,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.692,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.445,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 4.203,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 11.815,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 24.159,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 1.293,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 2.629,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 5.235,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 13.079,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 26.607,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 13.543,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 27.615,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 47.487,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 106.623,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 204.543,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 15.071,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 32.671,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 68.351,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 119.039,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 421.631,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 31.967,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 63.455,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 92.735,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 139.135,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 195.071,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 196.223,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 511.487,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 1050.623,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 36634.623,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 53805.055,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 35.037642927,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 214,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.025303134,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 13,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 4.330741888,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 119.96484375,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 44,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 2.213879181,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 257,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 23,
            "unit": "count"
          }
        ]
      }
    ]
  }
}