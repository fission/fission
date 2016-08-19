Fission
=======

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.

It's heavily customizable (but with sensible defaults), extensible to
any language, and interoperates well with other infrastructure.

You can create and edit functions throughs a web UI, or you can use
your favorite editor and upload them using a CLI.  You can trigger these
functions via HTTP requests to paths, or by timers.

Try it out at http://demo.example.com

Running Fission
===============

On your own Kubernetes cluster
------------------------------

### Setup Kubernetes

  https://github.com/kubernetes/minikube

### Get and Run Fission

  $ wget http://example.com/fission.yaml
  $ kubectl create -f fission.yaml


Status
======

Fission is in early alpha.


Performance
===========

The goal is to have less than 1 second latency for requests that don't
have a running instance.  In practice, benchmarks show this latency at
TODO msec for the NodeJS environment. 

Here's a simple hello world benchmark on NodeJS: TODO.  It achieves
TODO requests per second on a TODO-node Kubernetes cluster.


Contributions
=============

TODO
