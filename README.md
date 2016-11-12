Fission
=======

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.

It's customizable (with sensible defaults), extensible to any
language, and interoperates well with other infrastructure.

See http://fission.io for more.


Running Fission
===============

### Setup Kubernetes

On your laptop:

```
  https://github.com/kubernetes/minikube
```

Or use Google Container Engine to get a cluster.


### Verify access to the cluster

```
  $ kubectl version
```

### Get and Run Fission

If you're using GKE, use the fission.yaml unmodified.  If you're using
minikube, change all instances of LoadBalancer services to NodePort.

```
  $ curl http://fission.io/fission.yaml | kubectl create -f -

  $ kubectl --namespace fission get services
```

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.  FISSION_URL is used by
the fission CLI to find the server.  (FISSION_ROUTER is only needed
for the examples below to work.)

### Install the client CLI

```
  $ curl http://fission.io/fission > fission

  $ chmod +x fission

  $ sudo mv fission /usr/local/bin/
```

### Run an example

```
  $ fission env create --name nodejs --image fission/node-env
  
  $ echo 'module.exports = function(context, callback) { callback(200, "Hello, world!\n"); }' > hello.js  

  $ fission function create --name hello --env nodejs --code hello.js
  
  $ fission route create --method GET --url /hello --function hello
  
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```

Compiling Fission
=================

You'll need go installed, along with the glide dependecy management
tool.  You'll also need docker for building images.

The server side is compiled as one binary ("fission-bundle") which
contains controller, poolmgr and router; it invokes the right one
based on command-line arguments.

To build fission-bundle: clone this repo, then from the top level
directory:

```
  # Get dependencies
  $ glide install

  # Build fission server and an image
  $ pushd fission-bundle
  $ ./build.sh

  # Edit push.sh to point to your registry
  $ $EDITOR push.sh
  $ ./push.sh
  $ popd

  # To install, update fission.yaml to point to your compiled image
  $ $EDITOR fission.yaml
  $ kubectl create -f fission.yaml
```

If you're changing the CLI:

```
  # Build Fission CLI
  $ cd fission-cli && go install
```

Status
======

Fission is in early alpha.  Don't use it in production just yet, but
play with it and give us feedback!




Performance
===========

The alpha release has focussed on cold-start latency performance.  For
requests that don't have a running instance, i.e. a "cold start",
fission has a latency overhead of less than 100 msec for the NodeJS
environment.

