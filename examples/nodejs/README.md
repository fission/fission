# Fission Node.js Examples

This directory contains several examples to get you started using Node.js with Fission.

Before running any of these functions, make sure you have created a `nodejs` Fission environment:

```
$ fission env create --name nodejs --image fission/node-env
```

## Function signature

Every Node.js function has the same basic form:

```javascript
module.exports = async function(context) {
    return {
        status: 200,
        body: 'Your body here',
        headers: {
            'Foo': 'Bar'
        }
    }    
}
```

Since it is an `async` function, you can `await` `Promise`s, as demonstrated in the `stock.js` function.

## hello.js

This is a basic "Hello, World!" example. It simply returns a status of `200` and text body.

### Usage

```bash
# Upload your function code to fission
$ fission function create --name hello --env nodejs --code hello.js

# Map GET /hello to your new function
$ fission route create --method GET --url /hello --function hello

# Run the function.
$ curl http://$FISSION_ROUTER/hello
Hello, world!
```

## hello-callback.js

This is a basic "Hello, World!" example implemented with the legacy callback implementation. If you declare your function with two arguments (`context`, `callback`), a callback taking three arguments (`status`, `body`, `headers`) is provided.

⚠️️ Callback support is only provided for backwards compatibility! We recommend that you use `async` functions instead.

### Usage

```bash
# Upload your function code to fission
$ fission function create --name hello-callback --env nodejs --code hello-callback.js

# Map GET /hello-callback to your new function
$ fission route create --method GET --url /hello-callback --function hello-callback

# Run the function.
$ curl http://$FISSION_ROUTER/hello-callback
Hello, world!
```

## stock.js

This is a basic example of how you can easily use asynchronous requests in your functions. By default, the Node.js environment makes the [`request-promise-native`](https://github.com/request/request-promise-native) library available. In this example, the Google Finance API is used to determine when a stock was last traded.

### Usage

```bash
# Upload your function code to fission
$ fission function create --name stock --env nodejs --code stock.js

# Map GET /stock to your new function
$ fission route create --method GET --url /stock --function stock

# Run the function.
$ curl -H "Content-Type: application/json" -X POST -d '{"symbol":"AAPL"}' http://$FISSION_ROUTER/stock

{"text":"AAPL last traded at 138.99"}
```

## kubeEventsSlack.js

This example watches Kubernetes events and sends them to a Slack channel. To use this, create an incoming webhook for your Slack channel, and replace the `slackWebhookPath` in the example code.

### Usage

```bash
# Upload your function code to fission
$ fission fn create --name kubeEventsSlack --env nodejs --code kubeEventsSlack.js

# Watch all services in the default namespace:
$ fission watch create --function kubeEventsSlack --type service --ns default
```
