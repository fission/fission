---
title: Fission
weight: 1
---

Fission: Serverless Functions for Kubernetes
============================================

[fission.io](http://fission.io)  [@fissionio](http://twitter.com/fissionio)

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


Fission Concepts
----------------

A _function_ is a piece of code that follows the fission function
interface.

An _environment_ contains the language- and runtime-specific parts of
running a function.  

The following environments are currently available:
 

| Environment                          | Image                     |
| ------------------------------------ | ------------------------- |
| Binary (for executables or scripts)  | `fission/binary-env`      |
| Go                                   | `fission/go-env`          |
| .NET                                 | `fission/dotnet-env`      |
| .NET 2.0                             | `fission/dotnet20-env`    |
| NodeJS (Alpine)                      | `fission/node-env`        |
| NodeJS (Debian)                      | `fission/node-env-debian` |
| Perl                                 | `fission/perl-env`        |
| PHP 7                                | `fission/php-env`         |
| Python 3                             | `fission/python-env`      |
| Ruby                                 | `fission/ruby-env`        |


You can also extend environments or create entirely new
ones if you want.  (An environment is essentially just a container
with a webserver and dynamic loader.)

A _trigger_ is something that maps an event to a function; Fission
supports HTTP routes as triggers today, with upcoming support for
other types of event triggers, such as timers and Kubernetes events.

Usage
-----

```bash

  # Add the stock NodeJS env to your Fission deployment
  $ fission env create --name nodejs --image fission/node-env

  # A javascript one-liner that prints "hello world"
  $ curl https://raw.githubusercontent.com/fission/fission/master/examples/nodejs/hello.js > hello.js

  # Upload your function code to fission
  $ fission function create --name hello --env nodejs --code hello.js

  # Map GET /hello to your new function
  $ fission route create --method GET --url /hello --function hello

  # Run the function.  This takes about 100msec the first time.
  $ curl http://$FISSION_ROUTER/hello
  Hello, world!
```

See the [examples](examples) directory for more.