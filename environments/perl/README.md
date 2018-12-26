# Fission Perl environment

This is an environment to execute perl code.

It is implemented using the lightweight web application framework
[Dancer2](https://metacpan.org/pod/Dancer2) and the async I/O webserver
[Twiggy](https://metacpan.org/pod/Twiggy).

Since Twiggy is implemented with [AnyEvent](https://metacpan.org/pod/AnyEvent),
you can write async code using AnyEvent in your function.

## Build this image
   
```
docker build -t USER/perl . && docker push USER/perl
```

## Usage

The function you upload is actually a perl package returning a coderef (see the
example).

You can use other packages and add more subs, but only the coderef you returned
from the package is executed directly.

Your main-function gets exactly one argument: the Dancer2 request object. You
may want to `use Dancer2` to get functions like `send_error`.

## Example

```perl
# hello.pm
return sub {
    return 'Hello, Perl!';
}
```
