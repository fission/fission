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

By default, this chart installs additional, dependent charts:

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

### Upgrade from 1.15.x to 1.16.x

If you have been using `prometheus.enabled=true` in your fission-all chart, you will need to deploy the prometheus using prometheus community supported chart.
We have removed prometheus dependency from fission-all chart.
We would recommend [prometheus-community/prometheus](https://artifacthub.io/packages/helm/prometheus-community/prometheus) or [prometheus-community/kube-prometheus-stack](https://artifacthub.io/packages/helm/prometheus-community/kube-prometheus-stack) chart.

### Upgrade from 1.14.x to 1.15.x

With 1.15.x release, following changes are made:

- `fission-core` chart is removed
- `fission-all` chart is made similar `fission-core` chart
- In the `fission-all` chart, the following components are disabled which were enabled by default earlier. If you want to enable them, please use `--set` flag.

  - nats - Set `nats.enabled=true` to enable Fission Nats integration
  - influxdb - Set `influxdb.enabled=true` to enable Fission InfluxDB and logger component
  - prometheus - Set `prometheus.enabled=true` to install Prometheus with Fission
  - canaryDeployment - Set `canaryDeployment.enabled=true` to enable Canary Deployment

## Migrating from fission-core chart

With the release of Fission v1.15.x, the fission-core chart was removed.
Fission-all is now exactly similar to fission-core and can be used to migrate from fission-core.

If you are upgrading from the fission-core chart, you can use the following command to migrate with required changes.

```console
 helm upgrade [RELEASE_NAME] fission-charts/fission-all --namespace fission
 ```

## Configuration

See [Customizing the Chart Before Installing](https://helm.sh/docs/intro/using_helm/#customizing-the-chart-before-installing). To see all configurable options with detailed comments:

```console
helm show values fission-charts/fission-all
```

You may also `helm show values` on this chart's [dependencies](#dependencies) for additional options.

### Multiple releases

The same chart can be used to run multiple Fission instances in the same cluster if required.
