---
title: "First example"
date: 2017-09-07T20:10:05-07:00
draft: false
weight: 25
---

### Run an example

Finally, you're ready to use Fission!

```
$ fission env create --name nodejs --image fission/node-env:0.5.0

$ curl -LO https://raw.githubusercontent.com/fission/fission/master/examples/nodejs/hello.js

$ fission function create --name hello --env nodejs --code hello.js

$ fission route create --method GET --url /hello --function hello

$ curl http://$FISSION_ROUTER/hello
Hello, world!
```

### What's next?

If something went wrong, we'd love to help -- please [drop by the
slack channel](http://slack.fission.io) and ask for help.

Check out the
[examples](https://github.com/fission/fission/tree/master/examples)
for some example functions.