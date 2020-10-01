This is an example of creating a deployment package with multiple
files including some static data in text file.

### Create an environment

```
fission env create --name python --image fission/python-env:0.4.0rc --version 2
```

### Create a zip file with all your files

```
zip -jr multifile.zip *.py *.txt
```

### Create a function

Since there are multiple files, you have to specify an _entrypoint_ to
for the function. Its format is `<file name>.<function name>`. In our
example, that's `main.main`, to run function `main` in `main.py`.

```
fission function create --name multifile --env python --code multifile.zip --entrypoint main.main
```

### Test it

```
fission function test --name multifile
```

You should see the "Hello, world" message.

## Updating the function

### Edit a file

```
echo "I said hellooooo!" > message.txt
```

### Update the deployment package

```
zip -jr multifile.zip *.py *.txt
```

### Update the function

```
fission function update --name multifile --code multifile.zip
```

### Test it

```
fission function test --name multifile
```

You should now see your new, edited message.
