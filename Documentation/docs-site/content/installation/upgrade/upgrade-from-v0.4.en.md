---
title: "Upgrading from v0.4.x to v0.5.0"
draft: false
weight: 33
---

## How to Upgrade

1. Get the 0.5.0 CLI
2. Upgrade to Fission 0.5.0

### Get the new CLI

#### OS X

``` bash
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.5.0/fission-cli-osx && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Linux

``` bash
$ curl -Lo fission https://github.com/fission/fission/releases/download/0.5.0/fission-cli-linux && chmod +x fission && sudo mv fission /usr/local/bin/
```

#### Windows

For Windows, you can use the linux binary on WSL. Or you can download
this windows executable: [fission.exe](https://github.com/fission/fission/releases/download/0.5.0/fission-cli-windows.exe)

### Upgrade to Fission 0.5.0

Upgrade fission with a command similar to this:



``` bash
# find the release want to upgrade
$ helm list

# upgrade to 0.5.0
$ helm upgrade <release_name> https://github.com/fission/fission/releases/download/0.5.0/fission-all-0.5.0.tgz
```
