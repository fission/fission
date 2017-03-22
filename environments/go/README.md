# Fission: Go Environment

This is the Go environment for Fission.

It's a Docker image containing a Go 1.8rc3 runtime, along with a dynamic loader.

## Build this image

```
docker build -t USER/go-runtime . && docker push USER/go-runtime
```

## Using the image in fission

You can add this customized image to fission with "fission env
create":

```
fission env create --name go-runtime --image USER/go-runtime
```

Or, if you already have an environment, you can update its image:

```
fission env update --name go-runtime --image USER/go-runtime   
```

After this, fission functions that have the env parameter set to the
same environment name as this command will use this environment.
