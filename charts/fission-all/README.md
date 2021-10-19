# fission-all

[Fission](https://fission.io/) is a framework for serverless functions on Kubernetes.

## Prerequisites

- Kubernetes 1.19+
- Helm 3+

## Get Repo Info

```console
helm repo add fission-charts https://fission.github.io/fission-charts
helm repo update
```

_See [helm repo](https://helm.sh/docs/helm/helm_repo/) for command documentation._

## Install Chart

Replace `{{version}}` with [the latest Fission version](https://github.com/fission/fission/releases/latest).
![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/fission/fission)

```console
# Helm
$ export FISSION_NAMESPACE="fission"
$ kubectl create namespace $FISSION_NAMESPACE
$ kubectl create -k "github.com/fission/fission/crds/v1?ref={{version}}"
$ helm install [RELEASE_NAME] fission-charts/fission-all --namespace fission
```

_See [configuration](#configuration) below._

_See [helm install](https://helm.sh/docs/helm/helm_install/) for command documentation._

## Dependencies

By default this chart installs additional, dependent charts:

- [prometheus-community/prometheus](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus)

To disable dependencies during installation, see [multiple releases](#multiple-releases) below.

_See [helm dependency](https://helm.sh/docs/helm/helm_dependency/) for command documentation._

## Uninstall Chart

```console
# Helm
$ helm uninstall [RELEASE_NAME]
```

This removes all the Kubernetes components associated with the chart and deletes the release.

_See [helm uninstall](https://helm.sh/docs/helm/helm_uninstall/) for command documentation._

CRDs are not removed by this chart and should be manually cleaned up:

`{{version}}` references the version you used during the installation of chart.

```console
kubectl delete -k "github.com/fission/fission/crds/v1?ref={{version}}"
```

OR

You can list all Fission CRDs and clean them up with `kubectl delete crd` command.

```console
kubectl get crds| grep ".fission.io"
```

## Upgrading Chart

CRDs created by this chart are not updated by default and should be manually updated.

`{{version}}` references the version you are upgrading to ![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/fission/fission)

```console
kubectl replace -k "github.com/fission/fission/crds/v1?ref={{version}}"
```

```console
# Helm
$ helm upgrade [RELEASE_NAME] fission-charts/fission-all
```

_See [configuration](#configuration) below._

_See [helm upgrade](https://helm.sh/docs/helm/helm_upgrade/) for command documentation._

### Upgrading an existing Release to a new major version

A major chart version change (like v1.2.3 -> v2.0.0) indicates that there is an incompatible breaking change needing manual actions.

## Configuration

See [Customizing the Chart Before Installing](https://helm.sh/docs/intro/using_helm/#customizing-the-chart-before-installing). To see all configurable options with detailed comments:

```console
helm show values fission-charts/fission-all
```

You may also `helm show values` on this chart's [dependencies](#dependencies) for additional options.

### Multiple releases

The same chart can be used to run multiple Fission instances in the same cluster if required. To disable a dependency during installation, set `prometheus.enabled` to `false`.
