# Fission: Python Environment

This is the Python environment for Fission.

It's a Docker image containing a Python 3.5 runtime, along with a
dynamic loader.  A few common dependencies are included in the
requirements.txt file.

## Customizing this image

To add package dependencies, edit requirements.txt to add what you
need, and rebuild this image (instructions below).

You also may want to customize what's available to the function in its
request context.  You can do this by editing server.py (see the
comment in that file about customizing request context).

## Rebuilding and pushing the image

You'll need access to a Docker registry to push the image: you can
sign up for Docker hub at hub.docker.com, or use registries from
gcr.io, quay.io, etc.  Let's assume you're using a docker hub account
called USER.  Build and push the image to the the registry:

```
   docker build -t USER/python-env . && docker push USER/python-env
```

## Using the image in fission

You can add this customized image to fission with "fission env
create":

```
   fission env create --name python --image USER/python-env
```

Or, if you already have an environment, you can update its image:

```
   fission env update --name python --image USER/python-env   
```

After this, fission functions that have the env parameter set to the
same environment name as this command will use this environment.
