window.BENCHMARK_DATA = {
  "lastUpdate": 1787561867719,
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
      }
    ]
  }
}