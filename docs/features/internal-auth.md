# Internal auth (HMAC) — disabled by default on this fork

Upstream Fission added HMAC signing for its internal control-plane channels
(storagesvc `/v1/archive`, fetcher, builder, executor, router-internal). It is
gated by the Helm value `internalAuth.enabled`, which **upstream defaults to
`true`**.

**This fork defaults it to `false`.**

## Why

The HMAC verifier on `router-internal:8889` (and on storagesvc) rejects any
request that isn't signed with `FISSION_INTERNAL_AUTH_SECRET`. Two integrations
this fork relies on send **unsigned** requests and therefore fail with `401`
when internal auth is on:

- the upstream **KEDA HTTP connectors** (`ghcr.io/fission/keda-*-http-connector`),
  which invoke functions through `router-internal` — so MessageQueueTrigger
  delivery (RabbitMQ, Kafka, …) silently stops (messages pile up unacked);
- the **GraphQL federation gateway**, which makes unsigned subgraph calls.

Neither image signs its requests, and rebuilding them with signing support is a
separate effort. This fork ran its entire v1.22.x life with no HMAC at all, so
defaulting it off preserves that working behaviour and the same security posture
(NetworkPolicies + namespace isolation still apply).

## Enabling it anyway

```bash
helm upgrade fission ... --set internalAuth.enabled=true
```

If you enable it, you must also make every internal HTTP caller sign — i.e. use
signing-aware KEDA connector images (fork `fission/keda-connectors`, wrap the
transport with `hmacauth.ServiceSigner(ServiceRouterInternal)`, and pass
`FISSION_INTERNAL_AUTH_SECRET` to the connector Deployment via
`pkg/mqtrigger/scalermanager.go`), and likewise for the federation gateway.

## Operational note (all-or-nothing)

`internalAuth.enabled` drives **8 components** (storagesvc, buildermgr, executor,
router, timer, kubewatcher, and the kafka/keda MQ triggers). Toggling it requires
restarting **all** of them — a partial restart leaves verifiers and signers in a
split state (e.g. storagesvc still enforcing while a fetcher no longer signs),
which surfaces as fetch failures and executor specialization timeouts:

```bash
helm upgrade fission ... --set internalAuth.enabled=<true|false>
kubectl rollout restart deployment -n fission   # ALL fission deployments
```
