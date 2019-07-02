# Tensorflow Serving Environment Example

## Create Environment 

```bash
$ fission env create --name tensorflow --image fission/tensorflow-serving --version 2
```

## Create Package

```bash
$ zip -r half_plus_two.zip ./half_plus_two
$ fission pkg create --env tensorflow --deploy half_plus_two.zip
```

## Create Function

Here, the `--entrypoint` represents the name of top directory contains trained model.

```bash
$ fission fn create --name t1 --pkg <pkg name> --env tensorflow --entrypoint "half_plus_two"
$ fission fn test --name t1 --body '{"instances": [1.0, 2.0, 0.0]}' --method POST
```
