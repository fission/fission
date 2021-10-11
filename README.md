<p align="center">
  <img src="https://fission.io/images/logo-gh.svg" width="300" />
  <br>
  <h1 align="center">Fission: Serverless Functions for Kubernetes</h1>
</p>

<p align="center">
  <a href="https://github.com/fission/fission/blob/master/LICENSE">
    <img alt="Fission Licence" src="https://img.shields.io/github/license/fission/fission">
  </a>
  <a href="https://github.com/fission/fission/releases">
    <img alt="Fission Releases" src="https://img.shields.io/github/release-pre/fission/fission.svg">
  </a>
  <a href="https://pkg.go.dev/github.com/fission/fission">
    <img alt="go.dev reference" src="https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white">
  </a>
  <a href="https://goreportcard.com/report/github.com/fission/fission">
    <img src="https://goreportcard.com/badge/github.com/fission/fission" alt="Go Report Card" />
  </a>
  <a href="https://github.com/fission/fission/graphs/contributors">
    <img alt="Fission contributors" src="https://img.shields.io/github/contributors/fission/fission">
  </a>
  <a href="https://github.com/fission/fission/commits/master">
    <img alt="Commit Activity" src="https://img.shields.io/github/commit-activity/m/fission/fission">
  </a>
  <br>
  <a href="https://fission.io/">
    <img alt="Fission website" src="https://img.shields.io/badge/website-fission.io-blue">
  </a>
  <a href="https://fission.io/slack">
    <img alt="Fission slack" src="https://badgen.net/badge/slack/Fission?icon=slack">
  </a>
  <a href="https://twitter.com/fissionio">
    <img alt="Fission twitter" src="https://img.shields.io/twitter/follow/fissionio?style=social">
  </a>
  <a href="https://github.com/fission/fission">
    <img alt="GitHub Repo stars" src="https://img.shields.io/github/stars/fission/fission?style=social">
  </a>
</p>

--------------

Fission is a fast serverless framework for Kubernetes with a focus on
developer productivity and high performance.

Fission operates on _just the code_: Docker and Kubernetes are
abstracted away under normal operation, though you can use both to
extend Fission if you want to.

Fission is extensible to any language; the core is written in Go, and
language-specific parts are isolated in something called
_environments_ (more below).  Fission currently supports NodeJS, Python, Ruby, Go, 
PHP, Bash, and any Linux executable, with more languages coming soon.

Table of Contents
=================
- [Table of Contents](#table-of-contents)
  - [Performance: 100msec cold start](#performance-100msec-cold-start)
  - [Kubernetes is the right place for Serverless](#kubernetes-is-the-right-place-for-serverless)
  - [Getting Started](#getting-started)
  - [Learn More](#learn-more)
  - [Contributing](#contributing)
  - [Sponsors](#sponsors)
- [License](#license)

## Performance: 100msec cold start

Fission maintains a pool of "warm" containers that each contain a
small dynamic loader.  When a function is first called,
i.e. "cold-started", a running container is chosen and the function is
loaded.  This pool is what makes Fission fast: cold-start latencies
are typically about 100msec.

## Kubernetes is the right place for Serverless

We're built on Kubernetes because we think any non-trivial app will
use a combination of serverless functions and more conventional
microservices, and Kubernetes is a great framework to bring these
together seamlessly.

Building on Kubernetes also means that anything you do for operations
on your Kubernetes cluster &mdash; such as monitoring or log
aggregation &mdash; also helps with ops on your Fission deployment.

## Getting Started

```bash
  # Add the stock NodeJS env to your Fission deployment
  $ fission env create --name nodejs --image fission/node-env

  # Create a function with a javascript one-liner that prints "hello world"
  $ fission function create --name hello --env nodejs --code https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js

  # Run the function.  This takes about 100msec the first time.
  $ fission function test --name hello
  Hello, world!
```

## Learn More

- Understand [Fission Concepts](https://fission.io/docs/concepts/).
- See the [installation guide](https://fission.io/docs/installation/) for installing and running Fission.
- You can learn more about Fission and get started from [Fission Docs](https://fission.io/docs).
- See the [troubleshooting guide](https://fission.io/docs/trouble-shooting/) for debugging your functions and Fission installation.

## Contributing

Check out the [contributing guide](CONTRIBUTING.md).

## Sponsors

The following companies, organizations, and individuals support Fission's ongoing maintenance and development. If you are using/contributing to Fission, we would be happy to list you here, please raise a Pull request.

<p>
  <a href="https://infracloud.io/"><img src="https://fission.io/sponsors/infracloud.png" alt="InfraCloud" height="70"></a>
  <a href="https://srcmesh.com/"><img src="https://fission.io/sponsors/srcmesh.png" alt="Srcmesh" height="70"></a>
  <a href="https://www.digitalocean.com/?utm_medium=opensource&utm_source=fissionio">
    <img src="https://opensource.nyc3.cdn.digitaloceanspaces.com/attribution/assets/PoweredByDO/DO_Powered_by_Badge_blue.svg" width="201px">
  </a>
</p>

# License

Fission is licensed under the Apache License 2.0 - see the [LICENSE](./LICENSE) file for details
