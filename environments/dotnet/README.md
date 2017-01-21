# Fission: dotnet C# Environment

This is a simple dotnet C# environment for Fission.

It's a Docker image containing the dotnet 1.1.0 runtime. The image 
uses Kestrel with Nancy to host the internal web server and uses 
Roslyn to compile the uploaded code.

The image supports compiling and running code with types defined in
mscorlib and does not at present support other library references.
One workaround for this would be to add the references to this project's
project.json file and rebuild the container.

The environment works via convention where you create a C# class
called Fission which has a static method named Run taking a single
parameter, a dictionary containing any querystring parameters.

Example of simplest possible class to be executed:

```
using System;
using System.Collections.Generic;

public class Fission {
	public static string Run(Dictionary<string, object> args) {
		return null;
	}
}
```

Please see examples below.

## Rebuilding and pushing the image

To rebuild the image you need either a computer with dotnet 1.1.0
installed or else you will have to map the source directory into a
container containing the dotnet 1.1.0 environment.

### Locally installed Dotnet 1.1.0

Simply move to the source directory in a terminal and run the ./build.sh script.

The script will restore dependencies, compile a release build and
and build the container. If you need to change the name of the container
simply change it in the script.

After the build finishes push the new image to a Docker registry using the 
standard procedure.

### Build in a container

Move to the directory containing the source and start the Docker container
with dotnet and mount the current directory to a build location:

```
docker run -it --rm -v $PWD:/build microsoft/dotnet
```

Move to the build directory inside the container and restore the packages:

```
cd /build 
dotnet restore
log  : Restoring packages for /source/project.json...
log  : Installing System.Net.WebSockets 4.0.0.
log  : Installing runtime.native.System.IO.Compression 4.1.0.
...
```

Compile and publish a release build of the source to the 'out' folder:

```
dotnet publish -c Release -o out
Publishing source for .NETCoreApp,Version
...
```
Exit the build container and build the Docker container on the local host:

```
exit
docker build -t USER/dotnet-env .
``` 
After the build finishes push the new image to a Docker registry using the 
standard procedure.

## Echo example

### Setup fission environment
First you need to setup the fission according to your cluster setup as 
specified here: https://github.com/fission/fission


### Create the class to run

Secondly you need to create a file /tmp/func.cs containing the following code:

```
using System;
using System.Collections.Generic;

public class Fission {
	public static string Run(Dictionary<string, object> args) {
		return (string)args["text"];
	}
}
``` 
### Run the example

Lastly to run the example:

```
$ fission env create --name dotnet --image fission/dotnet-env

$ fission function create --name echo --env dotnet --code /tmp/func.cs

$ fission route create --method GET --url /echo --function echo

$ curl http://$FISSION_ROUTER/echo?text=hello%20world!
  hello world
```

## Addition service example

### Setup fission environment
First you need to setup the fission according to your cluster setup as 
specified here: https://github.com/fission/fission


### Create the class to run

Secondly you need to create a file /tmp/func.cs containing the following code:

```
using System;
using System.Collections.Generic;

public class Fission {
	public static string Run(Dictionary<string, object> args) {
        var x = Convert.ToInt32(args["x"]);
        var y = Convert.ToInt32(args["y"]);
        return (x+y).ToString();
	}
}
``` 
### Run the example

Lastly to run the example:

```
$ fission env create --name dotnet --image fission/dotnet-env

$ fission function create --name addition --env dotnet --code /tmp/func.cs

$ fission route create --method GET --url /add --function addition

$ curl "http://$FISSION_ROUTER/add?x=30&y=12"
  42
```
