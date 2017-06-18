# Fission: Ruby Environment

This is the Ruby environment for Fission.

It's a Docker image containing a Ruby 2.4.1 runtime, along with a
dynamic loader.  A few common dependencies are included in the
Gemfile.

## Customizing this image

To add package dependencies, edit Gemfile to add what you
need, and rebuild this image (instructions below).

## Rebuilding and pushing the image

You'll need access to a Docker registry to push the image: you can
sign up for Docker hub at hub.docker.com, or use registries from
gcr.io, quay.io, etc.  Let's assume you're using a docker hub account
called USER.  Build and push the image to the the registry:

```
   docker build -t USER/ruby-env . && docker push USER/ruby-env
```

## Using the image in fission

You can add this customized image to fission with "fission env
create":

```
   fission env create --name ruby --image USER/ruby-env
```

Or, if you already have an environment, you can update its image:

```
   fission env update --name ruby --image USER/ruby-env
```

After this, fission functions that have the env parameter set to the
same environment name as this command will use this environment.
