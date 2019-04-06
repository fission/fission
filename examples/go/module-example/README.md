# Go module usage

1. Initialize your project

```bash
$ go mod init "<module>"
```

For example,

```bash
$ go mod init "github.com/fission/fission/examples/go/go-module-example"
```

2. Add dependencies

 * See [here](https://github.com/golang/go/wiki/Modules#daily-workflow)

3. Verify

```bash
$ go mod verify
```

4. Archive and create package as usual

```bash
$ zip -r go.zip .
    adding: go.mod (deflated 26%)
    adding: go.sum (deflated 1%)
    adding: README.md (deflated 37%)
    adding: main.go (deflated 30%)
    
$ fission pkg create --env go --src go.zip
```
