Fission: Serverless Functions for Kubernetes
============================================
[![Build Status](https://travis-ci.org/fission/fission.svg?branch=master)](https://travis-ci.org/fission/fission)
[![Go Report Card](https://goreportcard.com/badge/github.com/fission/fission)](https://goreportcard.com/report/github.com/fission/fission)
[![Fission Slack](http://slack.fission.io/badge.svg)](http://slack.fission.io)

[fission.io](http://fission.io) [@fissionio](http://twitter.com/fissionio)

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.

Fission operates on _just the code_: Docker and Kubernetes are
abstracted away under normal operation, though you can use both to
extend Fission if you want to.

Fission is extensible to any language; the core is written in Go, and
language-specific parts are isolated in something called
_environments_ (more below).  Fission currently supports NodeJS, Python, Ruby, Go, 
PHP, Bash, and any Linux executable, with more languages coming soon.

### Performance: 100msec cold start

Fission maintains a pool of "warm" containers that each contain a
small dynamic loader.  When a function is first called,
i.e. "cold-started", a running container is chosen and the function is
loaded.  This pool is what makes Fission fast: cold-start latencies
are typically about 100msec.

### Kubernetes is the right place for Serverless

We're built on Kubernetes because we think any non-trivial app will
use a combination of serverless functions and more conventional
microservices, and Kubernetes is a great framework to bring these
together seamlessly.

Building on Kubernetes also means that anything you do for operations
on your Kubernetes cluster &mdash; such as monitoring or log
aggregation &mdash; also helps with ops on your Fission deployment.

Getting started and documentation
===============================

You can learn more about Fission and get started from [Fission Docs](https://docs.fission.io). 
* See the [installation guide](https://docs.fission.io/installation/) for installing and running Fission.
* See the [troubleshooting guide](https://docs.fission.io/trouble-shooting/) for debugging your functions and Fission installation.

Contributing
=================

### Building Fission
See the [compilation guide](https://docs.fission.io/contributing/).

### Contact
Reach us on [slack](http://slack.fission.io) or
[twitter](https://twitter.com/fissionio).

Fission is a project by [many contributors](https://github.com/fission/fission/graphs/contributors).

### Community meeting 

A regular community meeting takes place every other Thursday at 08:30 AM PT (Pacific Time). [Convert to your local timezone](http://www.thetimezoneconverter.com/?t=08:30&tz=PT%20%28Pacific%20Time%29).

## Official Releases

Official releases of Fission can be found on [the releases page](https://github.com/fission/fission/releases). 
Please note that it is strongly recommended that you use official releases of Fission, as unreleased versions from 
the master branch are subject to changes and incompatibilities that will not be supported in the official releases. 
Builds from the master branch can have functionality changed and even removed at any time without compatibility support 
and without prior notice.

## Licensing

Fission is under the Apache 2.0 license.
