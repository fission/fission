# Binary Environment Examples

The `binary` runtime is a go server that uses a subprocess to invoke executables or execute shell scripts.

For more info read the [environment README](../../environments/binary/README.md).

## Requirements

First, set up your fission deployment with the binary environment.

```bash
fission env create --name binary-env --image fission/binary-env
```

## Example Usage

### hello.sh

`hello.sh` is an very basic shell script that returns `"Hello, World!"`.

```bash
# Upload the function to fission
fission function create --name hello --env binary-env --code hello.sh

# Map /hello to the hello function
fission route create --method GET --url /hello --function hello

# Run the function
curl http://$FISSION_ROUTER/hello
```

This should return a HTTP response with the body `Hello World!`

### echo.sh

`echo.sh` shows the the use of STDIN to read the request body, echoing the input back in the response.

```bash
# Upload the function to fission
fission function create --name echo --env binary-env --code echo.sh

# Map /hello to the hello function
fission route create --method POST --url /echo --function echo

# Run the function
curl -XPOST -d 'Echoooooo!'  http://$FISSION_ROUTER/echo
```

This should return a HTTP response with the body `... Echoooooo!`.

### headers.sh

`headers.sh` shows the access to the environment variables that hold the HTTP headers, returning the set HTTP headers.

```bash
# Upload the function to fission
fission function create --name headers --env binary-env --code headers.sh

# Map /hello to the hello function
fission route create --url /headers --function headers

# Run the function
curl -H 'X-FOO: BAR'  http://$FISSION_ROUTER/headers
```

This should return a HTTP response with the body `... Echoooooo!`.

### hello..go

This example shows the differences between using shell scripts and binaries. `hello.go` returns `Hello World!` + the
environment variables it received from the server.

```bash
# Build the function targeted at the right architecture
GOOS=linux GOARCH=386 go build -o hello-go-func hello.go

# Upload the function to fission
fission function create --name hello-go --env binary-env --code hello-go-func

# Map /hello to the hello function
fission route create --url /hello-go --function hello-go

# Run the function
curl -H 'X-GO: AWESOME!'  http://$FISSION_ROUTER/hello-go
```
