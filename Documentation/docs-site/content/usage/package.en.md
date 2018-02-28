---
title: "Packaging source code"
draft: false
weight: 46
---

### Creating source package

Before you create a package, you need to create an environment with associated builder image:

```
$ fission env create --name pythonsrc --image fission/python-env:latest --builder fission/python-builder:latest --mincpu 40 --maxcpu 80 --minmemory 64 --maxmemory 128 --poolsize 2
environment 'pythonsrc' created
```

Let's take a simple python function which has dependency on a python pyyaml module. We can specify the dependencies in requirements.txt and a simple command to build from source. The tree structure of directory and contents of the file looks like:

```
sourcepkg/
├── __init__.py
├── build.sh
├── requirements.txt
└── user.py
```
And the file contents:
```
$ cat user.py 
import sys
import yaml

document = """
  a: 1
  b:
    c: 3
    d: 4
"""

def main():
    return yaml.dump(yaml.load(document))

$ cat requirements.txt 
pyyaml

$ cat build.sh 
#!/bin/sh
pip3 install -r ${SRC_PKG}/requirements.txt -t ${SRC_PKG} && cp -r ${SRC_PKG} ${DEPLOY_PKG}

$zip -jr demo-src-pkg.zip sourcepkg/
  adding: __init__.py (stored 0%)
  adding: build.sh (deflated 24%)
  adding: requirements.txt (stored 0%)
  adding: user.py (deflated 25%)
```
Using the source archive creared in previous step, you can create a package in Fission:

```
$ fission package create --sourcearchive demo-src-pkg.zip --env pythonsrc --buildcmd "./build.sh"
Package 'demo-src-pkg-zip-8lwt' created
```

Since we are working with source package, we provided the build command. Once you create the package, the build process will start and you can check the build logs by getting information of the package:

```
$ fission pkg info --name demo-src-pkg-zip-8lwt
Name:        demo-src-pkg-zip-8lwt
Environment: pythonsrc
Status:      succeeded
Build Logs:
Collecting pyyaml (from -r /packages/demo-src-pkg-zip-8lwt-v57qil/requirements.txt (line 1))
  Using cached PyYAML-3.12.tar.gz
Installing collected packages: pyyaml
  Running setup.py install for pyyaml: started
    Running setup.py install for pyyaml: finished with status 'done'
Successfully installed pyyaml-3.12
```

Using the package above you can create the function. Since package already is associated with a source package, environment and build command, these will be ignored when creating a function. Only addition thing you will need to provide is the entrypoint. Assuming you hace created the route, the function should be reachable with successful output:

```
$ fission fn create --name srcpy --pkg demo-src-pkg-zip-8lwt --entrypoint "user.main"
function 'srcpy' created

$ curl http://$FISSION_ROUTER/srcpy
a: 1
b: {c: 3, d: 4}
```

### Creating deployment package

Before you create a package you need to create an environment with the builder image:
```
$ fission env create --name pythondeploy --image fission/python-env:latest --builder fission/python-builder:latest --mincpu 40 --maxcpu 80 --minmemory 64 --maxmemory 128 --poolsize 2
environment 'pythonsrc' created
```

We will use a simple Python example which outputs "Hello World!" in a directory to create a deployment archive:

```
$ cat testDir/hello.py
def main():
    return "Hello, world!"

$zip -jr demo-deploy-pkg.zip testDir/

```
Using the archive and environments created previously, you can create a package:

```
$ fission package create --deployarchive demo-deploy-pkg.zip --env pythondeploy
Package 'demo-deploy-pkg-zip-whzl' created
```

Since it is a deployment archive, there is no need to build it, hence the build logs for the package will be empty:

```
$ fission package info --name demo-deploy-pkg-zip-whzl
Name:        demo-deploy-pkg-zip-xlaw
Environment: pythondeploy2
Status:      succeeded
Build Logs:
```

Finally you can create a function with the package and test the function:

```
$fission fn create --name deploypy --pkg demo-deploy-pkg-zip-whzl --entrypoint "hello.main"

$curl http://$FISSION_ROUTER/deploypy
Hello, world!
```
