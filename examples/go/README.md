# Go examples

The `go` runtime uses the [`plugin` package](https://golang.org/pkg/plugin/) to dynamically load an HTTP handler.

## Requirements

First, [set up your fission deployment with the go environment](https://github.com/fission/fission/blob/master/environments/go/README.md).

To use this environment, download the [build helper
script](https://raw.githubusercontent.com/fission/fission/master/environments/go/builder/go-function-build).

The script must be run in the same directory as the function you're
building.

## Examples

### hello.go

`hello.go` is an very basic HTTP handler returning `"Hello, World!"`.

```
# Build the function as a plugin. Outputs result to 'function.so'.
$ go-function-build hello.go

# Upload the function to fission
$ fission function create --name hello --env go-runtime --package function.so

# Map /hello to the hello function
$ fission route create --method GET --url /hello --function hello

# Run the function
$ curl http://$FISSION_ROUTER/hello
Hello, World!
```
