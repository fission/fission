# Go examples

The `go` runtime uses the [`plugin` package](https://golang.org/pkg/plugin/) to dynamically load an HTTP handler.

## Requirements

- go 1.8
- A [go-runtime fission environment](environments/go/README.md)

## Examples

### hello.go

`hello.go` is an very basic HTTP handler returning `"Hello, World!"`.


```
# Build the function as a plugin
$ ./build.sh

# Upload the function to fission
$ fission function create --name hello --env go-runtime --package hello.so

# Map /hello to the hello function
$ fission route create --method GET --url /hello --function hello

# Run the function
$ curl http://$FISSION_ROUTER/hello
Hello, World!
```
