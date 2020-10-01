# Adding podspec for builder of environment

If you see the files in the `specs` directory you will get to know that we have just generated the
environment spec using the command

```
fission env create --name python --image fission/python-env --builder fission/python-builder --spec
```

and changed the env-python.yaml file to have below `podspec` for builder for the environment.

```
nodeSelector:
  machinecap: high
tolerations:
- key: "env"
  value: "prod"
  operator: "Equal"
  effect: "NoSchedule"

```

If we create this environment, the builder pod that will be created will have the `PodSpec` that
we have mentioned for builder in the environment spec.
