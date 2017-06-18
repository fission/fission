# Fission

[Fission](http://fission.io/) is a framework for serverless functions on Kubernetes.


## Prerequisites

- Kubernetes 1.6+ with Beta APIs and RBAC enabled


## Helm charts

The following table lists two helm charts for Fission.

| Parameter      | Description                            |
| ---------------| ---------------------------------------|
| `fission-core` | Try with essential features of Fission |
| `fission-all`  | Enjoy all cool features of Fission     |

## Installing the chart

To install the chart with the release name `my-release`,

```bash
$ helm install --name my-release fission-all
```

## Uninstalling the chart

To uninstall/delete chart,

```bash
$ helm delete my-release
```

## Configuration

The following table lists the configurable parameters of the Fission chart and their default values.

| Parameter           | Description                                | Default                  |
| ------------------- | ------------------------------------------ | ------------------------ |
| `serviceType`       | Type of service to use                     | `LoadBalancer`.          |
| `image`             | Fission image                              | `fission/fission-bundle` |
| `imageTag`          | Fission image tag                          | `alpha20170124`          |
| `controllerPort`    | Fission Controller Service Port            | `31313`                  |
| `routerPort`        | Fission Router Service Port                | `31314`                  |
| `functionNamespace` | Namespace for Fission functions            | `fission-function`       |
| `openshiftRBAC`     | RBAC for openshift                         | `false`                  |


* Extra configuration for `fission-all`

| Parameter              | Description                 | Default                    |
| ---------------------- | --------------------------- | -------------------------- |
| `loggerInfluxdbAdmin`  | Log database admin username | `admin`.                   |
| `loggerFluentdImage`   | Logger fluentd image        | `fission/fluentd`          |
| `fissionUiImage`       | Fission ui image            | `fission/fission-ui:0.1.0` |


Specify each parameter using the `--set key=value[,key=value]` argument to `helm install`. For example,

```bash
$ helm install --name my-release --set image=custom/fission-bundle,imageTag=v1 fission-all
```

If you're using minikube, set serviceType to NodePort:

```bash
$ helm install --name my-release --set serviceType=NodePort fission-all
```

You can also set parameters with a yaml file (see values.yaml for
what it should look like):

```bash
$ helm install --name my-release -f values.yaml fission-all
```
