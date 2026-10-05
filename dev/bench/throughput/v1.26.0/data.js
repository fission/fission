window.BENCHMARK_DATA = {
  "lastUpdate": 1791190954881,
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
        "date": 1787561871049,
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
            "value": 3078.3166666666666,
            "unit": "rps"
          },
          {
            "name": "warm-path/endpointcache_hit_ratio",
            "value": 1,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/throughput",
            "value": 2705.3,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c10_throughput",
            "value": 2302.2833333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c50_throughput",
            "value": 2855.1833333333334,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c100_throughput",
            "value": 2817.55,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c250_throughput",
            "value": 557.4,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c500_throughput",
            "value": 426.6166666666667,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps100_throughput",
            "value": 100,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps250_throughput",
            "value": 43.5,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps500_throughput",
            "value": 499.73333333333335,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps1000_throughput",
            "value": 921.0166666666667,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1KiB_throughput",
            "value": 2634.6833333333334,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/10KiB_throughput",
            "value": 2237.4333333333334,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/100KiB_throughput",
            "value": 682.9333333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1MiB_throughput",
            "value": 118.96666666666667,
            "unit": "rps"
          },
          {
            "name": "autoscale-newdeploy/max_replicas",
            "value": 5,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scaled",
            "value": 1,
            "unit": "ratio"
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
          "id": "89dc34df8852b5e15ae116bdb132e9e7e3f3af63",
          "message": "hack/run-tlc: bump the tla2tools pin for the 2026-09-10 rebuild (#3723)\n\nThe tlaplus project re-uploaded the v1.8.0 tla2tools.jar release asset on\n2026-09-10 (manifest Implementation-Version \"2.0 2026-09-10\", X-Git-Revision\nc3af5e2dcc6e54860e96b8e7adf7509d3f685d25), so the pinned SHA256 no longer\nmatches and the tlc job fails at the checksum step on every PR. Verified the\nnew jar per the note in the script (Main-class tlc2.TLC, Implementation-Vendor\nMicrosoft Corp., tlc2/TLC.class present, size matches the release asset) and\nbumped the pin.",
          "timestamp": "2026-09-11T16:37:35Z",
          "url": "https://github.com/fission/fission/commit/89dc34df8852b5e15ae116bdb132e9e7e3f3af63"
        },
        "date": 1789377780195,
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
            "value": 4140.183333333333,
            "unit": "rps"
          },
          {
            "name": "warm-path/endpointcache_hit_ratio",
            "value": 1,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/throughput",
            "value": 3592.016666666667,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c10_throughput",
            "value": 3099.233333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c50_throughput",
            "value": 3928.766666666667,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c100_throughput",
            "value": 3201.733333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c250_throughput",
            "value": 889.0166666666667,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c500_throughput",
            "value": 1898.65,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps100_throughput",
            "value": 100,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps250_throughput",
            "value": 247.76666666666668,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps500_throughput",
            "value": 499.8833333333333,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps1000_throughput",
            "value": 951.3833333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1KiB_throughput",
            "value": 2565.05,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/10KiB_throughput",
            "value": 719.1666666666666,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/100KiB_throughput",
            "value": 898.95,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1MiB_throughput",
            "value": 180.11666666666667,
            "unit": "rps"
          },
          {
            "name": "autoscale-newdeploy/max_replicas",
            "value": 5,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scaled",
            "value": 1,
            "unit": "ratio"
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
          "id": "254f4435e41d7bd244a62d53bdc099e995fdbe37",
          "message": "Carve feature-independent changes out of the agent runtime (#3710): env runtimeClassName, statesvc EventLog routes, MCP _meta tracing, environment egress NetworkPolicy (#3722)\n\n* chart: per-environment egress NetworkPolicy knob\n\nAdds environmentNetworkPolicy.{enabled,allowClusterDNS,extraEgress}\n(default off) templating an Egress-only NetworkPolicy that selects every\npod carrying the environmentName label (poolmgr + newdeploy env pods).\nDefault posture copies Agent Sandbox's SandboxTemplate default: allow\ngeneral internet egress, deny RFC1918 + link-local (which subsumes the\ncloud metadata IP), cluster DNS allowed unless disabled. No Go changes;\nper-agent egress stays out of scope (CNI-specific).\n\n* chart: document control-plane carve-outs and pin egress netpol namespace scope\n\nThe RFC1918 deny-list also blocks in-cluster control-plane traffic that\ncrosses the same pod/Service CIDRs: fetcher->storagesvc (package fetch,\ncold start) and function->statesvc.\nName those flows in the values.yaml/template comments as what an operator\nneeds to extraEgress-allowlist before enabling; the knob's default posture\nis unchanged (opt-in, off by default, per the Agent Sandbox reference\nposture).\n\nAlso add the namespace-scope render assertion the template's own comment\npromises: the policy must follow fission-function-ns (functionNamespace\nif set, else defaultNamespace), not the release namespace, or it silently\nno-ops.\n\n* chart: render environment-egress NetworkPolicy per function namespace\n\nA single copy in fission-function-ns silently left egress unenforced\nfor functions in additionalFissionNamespaces: NetworkPolicy podSelector\nonly matches pods in its own namespace, and pkg/utils\nNamespaceResolver.GetFunctionNS maps only the DEFAULT namespace through\nfunctionNamespace — every other resource namespace maps to itself.\n\nRange over the same static-tenancy namespace set router/role-dataplane.yaml\nalready uses (functionNamespace/defaultNamespace plus each\nadditionalFissionNamespaces entry) and render one policy per namespace.\nDocument in both the template and values.yaml that dynamic/cluster-tenancy\nnamespaces onboarded at runtime are still outside this knob's coverage,\nsince they're unknown at chart render time.\n\n* test(helm): tighten hasDNSRule to require the kube-system scope\n\nThe old check counted any egress rule mentioning port 53, regardless of\npeer — so a regression dropping the template's load-bearing kube-system\nnamespaceSelector (letting env pods egress to any self-labelled\nk8s-app=kube-dns pod in any namespace) still passed every test. Require\nthe namespaceSelector/podSelector pairing plus both UDP and TCP port 53\nin the same rule before counting it as the cluster-DNS rule.\n\n* chart: document dual-stack and NodeLocal DNSCache gaps in egress knob\n\nThe default posture's internet-allow rule is an IPv4-only 0.0.0.0/0\nipBlock: fail-closed on dual-stack clusters (no accidental IPv6 egress\nopens up) but IPv6 destinations stay unreachable regardless. Separately,\nNodeLocal DNSCache's well-known listener (169.254.20.10) falls inside\nthe link-local deny range, so allowClusterDNS=true's kube-system/\nkube-dns rule does not cover it and DNS breaks on clusters that run it.\nNeither changes the default posture; both note the extraEgress override\nan operator needs.\n\n* chart: extract fission.functionNamespaces helper\n\nrouter/role-dataplane.yaml and executor/networkpolicy-environment-egress.yaml\neach recomputed the same deduped function-namespace list (functionNamespace,\nfalling back to defaultNamespace, plus additionalFissionNamespaces) inline.\nFactor it into a named template, following the space-joined-list precedent\nfission.adoptSecretNamespaces already sets, and switch both call sites to it.\nRendered output is unchanged (verified with a before/after helm template diff).\n\n* charts: deny RFC6598/CGNAT (100.64.0.0/10) in environment egress policy\n\nenvironment-egress's ipBlock except-list covered RFC1918 and link-local\nbut omitted 100.64.0.0/10 (RFC 6598 Carrier-Grade NAT) -- EKS clusters\nrunning VPC CNI custom networking use this range for pod/Service CIDRs,\nso the deny-in-cluster posture silently failed to cover in-cluster\ntraffic on those clusters. Add it to the except-list, document it in\nvalues.yaml alongside the existing IPv4-only/NodeLocal DNSCache caveats,\nand update the helm test's expected except-list.\n\n* test(helm): reuse findAllNamed in findInNamespace\n\n* feat(env): opt-in RuntimeClassName for syscall-isolated pods (runtime + builder)\n\nAdds EnvironmentSpec.RuntimeClassName so an environment can opt its pods\ninto a syscall-isolating RuntimeClass (gVisor/Kata) without hand-building\na full Runtime.PodSpec override. RuntimeClassName already propagated via\nMergePodSpec, but was undiscoverable without this typed field.\n\nFill-if-nil via a shared pkg/executor/util.ApplyEnvRuntimeClass helper,\napplied unconditionally (outside the Runtime.PodSpec/Builder.PodSpec nil\nguards) at the three env-aware pod-spec sites: poolmgr genDeploymentSpec,\nnewdeploy getDeploymentSpec, and buildermgr's builder deployment. A\nRuntime.PodSpec/Builder.PodSpec-set RuntimeClassName always wins, since\nMergePodSpec runs first.\n\nValidation is struct-level CEL on EnvironmentSpec (DNS1123-label charset,\nmax 253 chars), mirrored in Go's EnvironmentSpec.Validate() so the CLI's\npre-flight check agrees with what the apiserver admits. Not applied to\ncontainer-executor functions (no env pod spec to apply it to).\n\n* feat(cli): fission env --runtime-class flag\n\n* feat(cli): accept --runtime-class as alias for --runtimeclass\n\n* test,docs(env): runtime-class reaches the pod template; usage docs\n\n* test(env): >253-char runtimeclass reject case; ungate CEL backstop from NODE_RUNTIME_IMAGE\n\n* test(integration): update RuntimeClassName CEL assertion to the reworded message\n\nThe apiserver CEL message changed in the apis commit (08ba29b1) that fixed its\nself-contradictory 'DNS1123 label ... max 253' wording; update\nTestEnvRuntimeClassCELRoundTrip's expected substring to match. (Missed integration\ntest — the Go-side validator test asserts only wantErr, so it did not catch this.)\n\n* apis,cli(env): reword the runtimeClassName validation message; leave an empty --runtime-class unset\n\n- The RuntimeClassName error message said 'valid DNS1123 label ... max 253\n  chars', which is self-contradictory because a DNS1123 label caps at 63.\n  Reword the Go validator and the CEL marker to describe the actual rule\n  (label charset, up to 253 chars) and note that dots are intentionally\n  excluded because the targeted RuntimeClasses are labels.\n- env create set RuntimeClassName to &\"\" on an explicit --runtime-class=\"\",\n  which server-side validation then rejected; leave the field unset instead,\n  matching env update's clear semantics.\n\n* feat(mcp): _meta trace-context extraction and execute_tool spans (MCP 2026-07-28)\n\n* test(mcp): wait for the tools/call server span to end before asserting\n\nCallTool returning means the CLIENT has the result; the server-side\notelhttp span ends only after the handler finishes writing the response\nstream, so an immediate rec.Ended() read raced that end under CI load\nand found only the initialize POST's span. Re-read via EventuallyWithT\nuntil the execute_tool span's parent lands.\n\n* feat(statesvc): scoped EventLog append/read/head routes for function pods\n\n* statesvc: bound the eventlog append body cap; reject '.' stream segments\n\nTwo review-battery findings:\n- The /v1/eventlog/append body cap is derived from the keyspace's MaxValueBytes\n  (MaxAppendEvents * MaxValueBytes * 4/3, ~85x amplification), but MaxValueBytes\n  was validated only as >= 0 and statesvc sets no other body limit — so a tenant\n  could force the shared statesvc to size an unbounded body, and at extreme\n  values the int64 multiplication overflowed. Bound MaxValueBytes at admission\n  (fv1.MaxStateMaxValueBytes = 4MiB, 16x the default) AND clamp the resolved cap\n  in the handler before deriving the body cap, so it stays bounded and overflow-\n  safe even for a keyspace stored before the bound existed.\n- stateapi.ValidStream rejected a '..' segment but not a bare '.' segment, so\n  'a/./b' aliased to the same internal stream as 'a/b'. Reject '.' too.\n\n* comments: make the carved-out code self-describing\n\nDrop cross-references to code and planning notes that do not exist on\nmain (the agent-runtime dispatcher, its tracing tests, and plan section\nids) from the chart, MCP tracing, statesvc, and env runtime-class test\ncomments. No behavior change.\n\n* statesvc: bound EventInput.Type; cover the eventlog auth matrix\n\n- Cap EventInput.Type at 1-128 bytes. The append-body envelope otherwise\n  left it bounded only by the whole-request LimitReader, so a caller could\n  spend the payload-sized budget on type-string bytes instead of the\n  per-event payload cap.\n- Add TestHandlerEventLogAuth, mirroring TestHandlerScopeForgery's KV-route\n  matrix on the eventlog routes (malformed scheme -> 401, unclaimed\n  keyspace -> 403), and a ValidStream case pinning that embedded dots\n  ('a..b') stay valid.\n- Make the postEventLog test helper generic so each call site's concrete\n  request type stays visible instead of being erased to any.\n\n* poolmgr: fold RuntimeClassName into the environment runtime hash\n\nRuntimeClassName lands in the generic pool template, but envRuntimeHash\ndid not cover it, so changing only that field left existing pool RSes and\nspecialized pods stamped with the old hash considered current: they kept\nserving under the previous RuntimeClass. Hash the effective value with\nomitempty so every environment that does not set the field keeps a\nbyte-identical hash (no pool roll on upgrade; the golden test still\npasses), while setting or switching it recycles warm pods.\n\n* statesvc: byte budgets for eventlog append/read; align admin, embedded, and CRD caps with the value ceiling\n\nThe per-event MaxValueBytes quota and the count caps (64 per append, 500\nper read page) did not bound bytes: 64 events at a 4MiB quota is 256MiB\nper append, and a 500-event page of 4MiB events materializes ~2GiB in the\nstore, the out slice, and the encoder of the shared statesvc.\n\n- stateapi.MaxAppendPayloadBytes (= the value ceiling) bounds the summed\n  payloads of one append; the handler answers 413 request_too_large for a\n  batch over it, and the reader is now an http.MaxBytesReader at the\n  derived wire cap so an oversized body is also a 413 rather than a\n  truncated-JSON 400.\n- stateapi.MaxReadPayloadBytes bounds a read page: the handler clamps the\n  page count to budget / <keyspace per-event cap> BEFORE asking the store,\n  so the bound holds in the store, not only on the wire. Sized so a\n  keyspace at the default cap keeps its full default page (128 >= 100).\n- The admin HMAC verifier buffered at most 512KiB, rejecting a valid\n  append or KV write before the handler ran; it now shares the handler's\n  wire cap (stateapi.MaxRequestBodyBytes).\n- The embedded statestore transport (httpapi.MaxRequestBytes) capped\n  requests at 4MiB, below a base64-inflated value at the 4MiB ceiling; it\n  is now derived from the ceiling and a test pins that it covers\n  statesvc's own cap, so embedded mode never rejects an admitted request.\n- The 4MiB ceiling was enforced only by StateConfig.Validate(), which the\n  admission webhook does not call for CEL-covered fields; add the matching\n  Maximum marker so kubectl/GitOps writers hit the same bound. Function and\n  FunctionVersion CRDs regenerated.\n\n* statestore: BoundedEventLog byte-bounded reads; statesvc reads pages by stored bytes\n\nA read page count derived from the keyspace's CURRENT MaxValueBytes cannot\nbound bytes: the quota is mutable and lowering it does not rewrite events\nappended under a larger one, so a stream of 4MiB events read after the\nquota dropped to 256KiB still asked the store for 128 of them (~512MiB).\n\n- Add the optional statestore.BoundedEventLog interface: ReadBounded is\n  Read under a payload-byte budget, enforced while scanning so the bound\n  holds in the store. At least one event is always returned so paging\n  never stalls; maxBytes <= 0 is plain Read. Implemented natively by the\n  memory driver, sqlstore (sqlite/postgres; the cursor closes at the\n  budget, so at most one event past it is scanned), the embedded HTTP\n  client/server (budget rides the request), and the metered wrapper;\n  statestore.ReadBounded falls back to read-then-trim for a driver that\n  lacks it. Conformance suite covers the contract for every driver.\n- statesvc eventRead now asks the store for MaxReadPayloadBytes of actual\n  stored payload instead of clamping the count by quota; the hard count cap\n  and default page are unchanged. Test lowers the quota after writing\n  ceiling-sized events and asserts the page ends at the budget.\n- The read/head request bodies are read through http.MaxBytesReader (shared\n  readJSONBody helper with append), so a valid document padded past the\n  cap is a 413 request_too_large instead of a silently truncated prefix\n  that still parses.\n\n* poolmgr: hash the effective RuntimeClass, not the dedicated field\n\nutil.ApplyEnvRuntimeClass is fill-if-nil: a Runtime.PodSpec override that\nnames a RuntimeClass wins over spec.runtimeClassName. Hashing the dedicated\nfield unconditionally therefore recycled every warm pool when only the\nshadowed field changed, although the pod template was unchanged. Hash the\nsame effective value the template gets (override, else dedicated field,\nelse empty); the unset case stays byte-identical.\n\n* statesvc: clamp legacy MaxValueBytes once in the index; regen swagger doc; drop a redundant conversion\n\n- FunctionIndex.Upsert clamps a stored MaxValueBytes to the admission\n  ceiling, so the KV PUT/CAS routes and the EventLog routes (and the body\n  caps derived from the quota) enforce one bound on both the bearer and the\n  admin path; the EventLog-only clamp in the handler goes away. A Function\n  stored before the ceiling existed can no longer admit a value the admin\n  HMAC path would reject.\n- Regenerate the swagger type doc for the MaxValueBytes comment change.\n- Remove the int64 conversions golangci-lint (unconvert) flagged in the\n  embedded transport cap test.\n\n* docs(rfc,specs): sync the EventLog and statesvc contracts with the shipped code\n\nNo protocol modeled by the TLA+ specs changes in this PR: the CAS-append\n(workflowfold), the version-CAS cursor subscription (eventlogsub), and\nthe MaxKeys counter race (quota) are untouched, and every direct EventLog\nconsumer stops on an empty page, so a byte-bounded page is a batching\ndetail outside the models. Record that honestly where the specs' scope is\ndocumented, and bring the RFC listings up to date:\n\n- eventlogsub.tla header and specs/README: read-page shape (a consumer's\n  Read batch, the optional BoundedEventLog page, the statesvc read route)\n  is not modeled; the progress guarantee (>= 1 event per non-empty page,\n  stop on empty never on short) is functional and lives in the driver\n  conformance suite. Per-request byte limits have no shared counter, so\n  quota.tla does not model them.\n- RFC-0021: note the shipped EventLog extensions (Head, BoundedEventLog).\n- RFC-0023: add the statesvc eventlog routes with their limits and the\n  MaxValueBytes admission ceiling / legacy clamp.\n\nAll green and negative TLC configs pass with the edited spec.",
          "timestamp": "2026-09-15T15:08:38Z",
          "url": "https://github.com/fission/fission/commit/254f4435e41d7bd244a62d53bdc099e995fdbe37"
        },
        "date": 1791190954200,
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
            "value": 3868.8333333333335,
            "unit": "rps"
          },
          {
            "name": "warm-path/endpointcache_hit_ratio",
            "value": 1,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/throughput",
            "value": 3260.3166666666666,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c10_throughput",
            "value": 2760.483333333333,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c50_throughput",
            "value": 3534.6666666666665,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c100_throughput",
            "value": 3608.6,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c250_throughput",
            "value": 850.3333333333334,
            "unit": "rps"
          },
          {
            "name": "concurrency-sweep/c500_throughput",
            "value": 662.4,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps100_throughput",
            "value": 99.68333333333334,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps250_throughput",
            "value": 53.31666666666667,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps500_throughput",
            "value": 499.85,
            "unit": "rps"
          },
          {
            "name": "rps-sweep/rps1000_throughput",
            "value": 981.7833333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1KiB_throughput",
            "value": 3267.5333333333333,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/10KiB_throughput",
            "value": 2851.6666666666665,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/100KiB_throughput",
            "value": 1449,
            "unit": "rps"
          },
          {
            "name": "payload-sweep/1MiB_throughput",
            "value": 142.58333333333334,
            "unit": "rps"
          },
          {
            "name": "autoscale-newdeploy/max_replicas",
            "value": 5,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scaled",
            "value": 1,
            "unit": "ratio"
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