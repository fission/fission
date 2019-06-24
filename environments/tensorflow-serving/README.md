# Fission: Tensorflow Serving Environment

This is the Tensorflow Serving environment for Fission.

It's a Docker image containing a Go runtime, along with a tensorflow serving service.

## How it works

Tensorflow Serving is an serving service that supports both RESTful API and gRPC endpoints. In current implementation,
Go server launches `tensorflow_model_server` to load in model during specialization. As long as the Go server receives
requests from router it creates a reverse proxy that connects to RESTful API endpoint exposed by tensorflow_model_server
and get response from the upstream server for user.

## Build this image

```
docker build -t USER/tensorflow-serving . && docker push USER/tensorflow-serving
```

## Using the image in fission

You can add this customized image to fission with "fission env create":

```
fission env create --name tensorflow --image USER/tensorflow-serving --version 2
```

After this, fission functions that have the env parameter set to the
same environment name as this command will use this environment.
