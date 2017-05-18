# Fission Roadmap

## Function features (area-func)

- Secrets, configmaps, env vars
- Volumes
- Mem requests and limits
- Function exec time deadline
- Expose a regular K8s Service for a function

## Fission API (area-api)

- TPR-based controller
- API authentication
- Aggregated API server

## Development Workflows (area-dev)

- Function Versioning
- Versioning for a group of functions
- Rolling upgrades
  - for one function
  - for multiple functions
  - for functions + other kubernetes deployments
- Unit testing

## API Gateway / Ingress features (area-ingress)

- Function authn hooks
- K8s Ingress flag
- Bundle an ingress controller?

## Workflows and Function composition (area-composition)

- Simple Composition - Sync
- Simple Composition - Async
- Hooks (pre, post, on-error)
- Workflows
- Testing in the presence of function composition

## Events (area-events)

- NATS
- Kafka
- AWS: SNS, SQS
- Google PubSub
- RabbitMQ
- Other event sources

- Bundle an event queue (NATS streaming, most probably)

## Operability (area-ops)

### Fission Install/Upgrade (area-install)

- Helm Installer for Fission
- Helm installation for fission functions
- CLI installer/upgrader? ("fission upgrade")
- Upgrade checker/reminder (like minikube does)
- CLI auto-upgrader

### Function observation (area-observe)

- Function Logging
- Tracing -- Opentracing
- Metrics -- Prometheus
- Function exception tracking
- Logging function load errors
- Tracing fission overheads for each function

## Function Security

- Function isolation (is authz hook sufficient or do we need something like mutual TLS?)

## UX, especially for beginners (area-ux)

- Fission CLI should include a tutorial
- Fission CLI should have a way to drop you into the UI
- Eliminate FISSION_URL, just use kube client to find fission url.  Also useful to grab credentials.

## Documentation (area-doc)

- Installation guide improvements
- Troubleshooting guide for common problems
- FAQ
- Performance overview
- Render docs nicely to fission.io

## Web UI (tracked separately in the fission-ui repo)

## Performance and Scalability (area-perf)

- Autoscaling
- Cold-start optimization -- optimistically choose from pool, save about ~20msec
- Cold-start optimization -- preload funcs in fetcher
- Cold-start optimization -- preload libraries in envs (v2) -- mem vs. speed tradeoff

## Function extensibility (area-ext)

- Env v2: easy addition of dependencies etc.
- Integration with Service Broker

## Multi-area stuff

- Execution strategies: cold-start pool vs create-pod-on-cold-start -- one size doesn't fit all, at least with current tech; abstract over execution strategies according to requirements
