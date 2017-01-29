# Fission

[Fission](http://fission.io/) is a framework for serverless functions on Kubernetes.


## Prerequisites

- Kubernetes 1.4+ with Beta APIs enabled


## Installing the chart

To install the chart with the release name `my-release`,

```bash
$ helm install --name my-release ./fission
```

## Uninstalling the chart

To uninstall/delete chart,

```bash
$ helm delete my-release
```

## Configuration
The following tables lists the configurable parameters of the Fission chart and their default values.

| Parameter                  | Description                                | Default                                                    |
| -----------------------    | ----------------------------------         | ---------------------------------------------------------- |
| `serviceType`              | Type service to use                        | `LoadBalancer`. If minikube `NodePort`                     |
| `image`                    | Fission image repository                   | `fission/fission-bundle`                                   |
| `imageTag`                 | Fission image version                      | `alpha20170124`                                            |
| `controllerPort`           | Fission Controller Service Port            | `31313`                                                    |
| `routerPort`               | Fission Router Service Port                | `31314`                                                    |

Specify each parameter using the `--set key=value[,key=value]` argument to `helm install`. For example,

```bash
$ helm install --name my-release \
  --set image=custom/fission-bundle,imageTag=v1 \
    fission
```

Default values.yml can also ignored with custom file.
```bash
$ helm install --name my-release -f values.yaml stable/postgresql
```

For reference check [values.yaml](values.yaml).




