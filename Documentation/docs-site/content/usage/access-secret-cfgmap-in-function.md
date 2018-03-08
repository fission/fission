---
title: "Accessing Secrets in Functions"
draft: false
weight: 47
---

Functions can access Kubernetes
[Secrets](https://kubernetes.io/docs/concepts/configuration/secret/)
and
[ConfigMaps](https://kubernetes.io/docs/concepts/storage/volumes/#configmap).

Use secrets for things like API keys, authentication tokens, and so
on.

Use config maps for any other configuration that doesn't need to be a
secret.

### Create A Secret or a ConfigMap

You can create a Secret or ConfigMap with the Kubernetes CLI:

``` bash
$ kubectl -n default create secret generic my-secret --from-literal=TEST_KEY="TESTVALUE"

$ kubectl -n default create configmap my-configmap --from-literal=TEST_KEY="TESTVALUE"
```

Or, use `kubectl create -f <filename.yaml>` to create these from a YAML file.

``` yaml
apiVersion: v1
kind: Secret
metadata:
  namespace: default
  name: my-secret
data:
  TEST_KEY: VEVTVFZBTFVF # value after base64 encode
type: Opaque

---
apiVersion: v1
kind: ConfigMap
metadata:
  namespace: default
  name: my-configmap
data:
  TEST_KEY: TESTVALUE
```

### Accessing Secrets and ConfigMaps

Secrets and configmaps are accessed similarly.  Each secret or
configmap is a set of key value pairs. Fission sets these up as files
you can read from your function.

``` bash
# Secret path
/secrets/<namespace>/<name>/<key>

# ConfigMap path
/configs/<namespace>/<name>/<key>
```

From the previous example, the paths are:

``` bash
# secret my-secret
/secrets/default/my-secret/TEST_KEY

# confimap my-configmap
/configs/default/my-configmap/TEST_KEY
```

Now, let's create a simple python function (leaker.py) that returns
the value of Secret `my-secret` and ConfigMap `my-configmap`.

``` python
# leaker.py

def main():
    path = "/configs/default/my-configmap/TEST_KEY"
    f = open(path, "r")
    config = f.read()

    path = "/secrets/default/my-secret/TEST_KEY"
    f = open(path, "r")
    secret = f.read()

    msg = "ConfigMap: %s\nSecret: %s" % (config, secret)

    return msg, 200
```


Create an environment and a function:

``` bash
# create python env
$ fission env create --name python --image fission/python-env

# create function named "leaker"
$ fission fn create --name leaker --env python --code leaker.py --secret my-secret --configmap my-configmap
```


Run the function, and the output should look like this:

``` bash
$ fission function test --name leaker
ConfigMap: TESTVALUE
Secret: TESTVALUE
```


{{% notice note %}}
If the Secret or ConfigMap value is updated, the function may
not get the updated value for some time; it may get a cached older
value.
{{% /notice %}}

