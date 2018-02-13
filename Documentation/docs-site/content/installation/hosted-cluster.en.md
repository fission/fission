---
title: "Hosted Cluster Installation"
date: 2017-09-07T20:10:05-07:00
draft: false
weight: 24
---

### Install Fission

#### Cloud hosted clusters (GKE, AWS, Azure etc.)

```
$ helm install --namespace fission https://github.com/fission/fission/releases/download/0.5.0/fission-all-0.5.0.tgz
```

#### Minimal version

The fission-all helm chart installs a full set of services including
the NATS message queue, influxDB for logs, etc. If you want a more
minimal setup, you can install the fission-core chart instead:

```
$ helm install --namespace fission https://github.com/fission/fission/releases/download/0.5.0/fission-core-0.5.0.tgz
```

### Install the Fission CLI

#### OS X

Get the CLI binary for Mac:

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.5.0/fission-cli-osx && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Linux

```
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.5.0/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Windows

For Windows, you can use the linux binary on WSL. Or you can download
this windows executable: [fission.exe](https://github.com/fission/fission/releases/download/0.5.0/fission-cli-windows.exe)

### Set environment vars

Set the FISSION_URL and FISSION_ROUTER environment variables.
FISSION_URL is used by the fission CLI to find the server.
(FISSION_ROUTER is only needed for the examples below to work.)

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.  Wait for services to
get IP addresses (check this with ```kubectl --namespace fission get
svc```).  Then:

##### AWS
```
  $ export FISSION_URL=http://$(kubectl --namespace fission get svc controller -o=jsonpath='{..hostname}')
  $ export FISSION_ROUTER=$(kubectl --namespace fission get svc router -o=jsonpath='{..hostname}')
```

##### GCP
```
  $ export FISSION_URL=http://$(kubectl --namespace fission get svc controller -o=jsonpath='{..ip}')
  $ export FISSION_ROUTER=$(kubectl --namespace fission get svc router -o=jsonpath='{..ip}')
```