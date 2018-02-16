---
title: "Function"
draft: false
weight: 41
---

## Create a function

Before creating a function the environment should be created, we will assume that we have already created environment named `node`. 

Let's create a simple code snippet in nodejs which will output Hello world:

```
module.exports = async function(context) {
    return {
        status: 200,
        body: "Hello, world!\n"
    };
}
```

Let's create a route for the function which can be used for making HTTP requests:

```
$ fission route create --function hello --url /hello
trigger '5327e9a7-6d87-4533-a4fb-c67f55b1e492' created
```


### Pool Based Executor

Let's create a function based on pool based executor.

```
fission fn create --name hello --code hello.js --env node--backend poolmgr
```

When we hit this function, we get a response:

```
$ curl http://$FISSION_ROUTER/hello
Hello, world!
```
### New Deployment Executor


```
fission fn create --name hello --code hello.js --env node --minscale 1 --maxscale 5  --backend poolmgr
```



### 