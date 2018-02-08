---
title: "Access secret/configmap in function"
draft: false
weight: 41
---

From fission v0.5.0 and later, functions are able to access [Secrets](https://kubernetes.io/docs/concepts/configuration/secret/) and [ConfigMaps](https://kubernetes.io/docs/concepts/storage/volumes/#configmap) specified by users.

### Create Secret and ConfigMap

You can create Secret and ConfigMap with CLI.

``` bash
$ kubectl -n default create secret generic foo --from-literal=TEST_KEY="TESTVALUE"
$ kubectl -n default create configmap bar --from-literal=TEST_KEY=TESTVALUE
```

Or use `kubectl create -f <filename.yaml>` to create these from a YAML file.

``` yaml
apiVersion: v1
kind: Secret
metadata:
  namespace: default
  name: foo
data:
  TEST_KEY: VEVTVFZBTFVF # value after base64 encode
type: Opaque

---
apiVersion: v1
kind: ConfigMap
metadata:
  namespace: default
  name: bar
data:
  TEST_KEY: TESTVALUE
```

### Access Secret and ConfigMap

Since content of Secret and ConfigMap are key-value pairs, functions can access them with following paths:

``` bash
# Secret path
/secrets/<namespace>/<name>/<key>

# ConfigMap path
/configs/<namespace>/<name>/<key>
```

From the previous example, the paths are:

``` bash
# secret foo
/secrets/default/foo/TEST_KEY

# confimap bar
/configs/default/bar/TEST_KEY
```

Now, let's create a simple python function (leaker.py) that return value of Secret `foo` and ConfigMap `bar`.

``` python
# leaker.py

def main():
    path = "/configs/default/bar/TEST_KEY"
    f = open(path, "r")
    config = f.read()

    path = "/secrets/default/foo/TEST_KEY"
    f = open(path, "r")
    secret = f.read()

    msg = "ConfigMap: %s\nSecret: %s" % (config, secret)

    return msg, 200
```


Create environment, function and http trigger.

``` bash
# create python env
$ fission env create --name python --image fission/python-env

# create function named "leaker"
$ fission fn create --name leaker --env python --code leaker.py --secret foo --configmap bar

# create route(http trigger)
$ fission route create --function leaker --url /leaker --method GET
```


Try to access the function, the output should look like following.

``` bash
$ curl http://$FISSION_ROUTER/leaker
ConfigMap: TESTVALUE
Secret: TESTVALUE
```

Note: If the Secret or ConfigMap value is updated, the function may not get the updated value for some time; it may get a cached older value.

