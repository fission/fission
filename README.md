Fission
=======

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.

It's customizable (with sensible defaults), extensible to any
language, and interoperates well with other infrastructure.

See https://fission.io for more.


Running Fission
===============

On your own Kubernetes cluster
------------------------------

### Setup Kubernetes

On your laptop:

  https://github.com/kubernetes/minikube

Or use Google Container Engine.

### Verify access to the cluster

  $ kubectl version

### Get and Run Fission

If you're using GKE, use the fission.yaml unmodified.  If you're using
minikube, change all instances of LoadBalancer services to NodePort.


  $ curl http://fission.io/fission.yaml | kubectl create -f -

  $ kubectl --namespace fission get services

Save the external IP addresses of controller and router services in
FISSION_URL and FISSION_ROUTER, respectively.

### Install the client CLI

  $ curl http://fission.io/fission > fission

  $ chmod +x fission

  $ sudo mv fission /usr/local/bin/

### Run an example

  $ fission env create --name nodejs --image fission/node-env
  
  $ echo 'module.exports = function(context, callback) { callback(200, "Hello, world!\n"); }' > hello.js  

  $ fission function create --name hello --env nodejs --code hello.js
  
  $ fission route create --method GET --url /hello --function hello
  
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!


Status
======

Fission is in early alpha.  Watch this space for announcements soon!


Performance
===========

The alpha release has focussed on cold-start latency performance.  For
requests that don't have a running instance, i.e. a "cold start",
fission has a latency overhead of less than 100 msec for the NodeJS
environment.

