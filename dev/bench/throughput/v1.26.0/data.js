window.BENCHMARK_DATA = {
  "lastUpdate": 1785143475524,
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
        "date": 1785143474772,
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
            "value": 2079.766666666667,
            "unit": "rps"
          },
          {
            "name": "warm-path/endpointcache_hit_ratio",
            "value": 1,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/throughput",
            "value": 1989.2833333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c10_throughput",
            "value": 1603.3666666666666,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c50_throughput",
            "value": 1931.5666666666666,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c100_throughput",
            "value": 1969.6333333333334,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c250_throughput",
            "value": 1953.9166666666667,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c500_throughput",
            "value": 275,
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
            "value": 499.75,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps1000_throughput",
            "value": 965.4333333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1KiB_throughput",
            "value": 1825.4833333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/10KiB_throughput",
            "value": 1619.8333333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/100KiB_throughput",
            "value": 676.3666666666667,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1MiB_throughput",
            "value": 0.016666666666666666,
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