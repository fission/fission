# Go examples

The `go` runtime uses the [`plugin` package](https://golang.org/pkg/plugin/) to dynamically load an HTTP handler.

## Requirements

First, set up your fission deployment with the go environment.

```
fission env create --name go-env --image fission/go-env:1.8.1
```

To ensure that you build functions using the same version as the
runtime, fission provides a docker image and helper script for
building functions.

## Example Usage

### hello.go

`hello.go` is an very basic HTTP handler returning `"Hello, World!"`.

```bash
# Download the build helper script
$ curl https://raw.githubusercontent.com/fission/fission/master/environments/go/builder/go-function-build > go-function-build
$ chmod +x go-function-build

# Build the function as a plugin. Outputs result to 'function.so'
$ go-function-build hello.go

# Upload the function to fission
$ fission function create --name hello --env go-env --package function.so

# Map /hello to the hello function
$ fission route create --method GET --url /hello --function hello

# Run the function
$ curl http://$FISSION_ROUTER/hello
Hello, World!
```
