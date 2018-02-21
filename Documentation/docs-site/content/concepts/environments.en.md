---
title: "Environment"
draft: false
weight: 32
---

An environment contains the language and runtime specific parts of a function. An environment is essentially a container with a webserver and a dynamic loader for the function code.

The following pre-built environments are currently available for use in Fission:
 
| Environment                          | Image                     |
| ------------------------------------ | ------------------------- |
| Binary (for executables or scripts)  | `fission/binary-env`      |
| Go                                   | `fission/go-env`          |
| .NET                                 | `fission/dotnet-env`      |
| .NET 2.0                             | `fission/dotnet20-env`    |
| NodeJS (Alpine)                      | `fission/node-env`        |
| NodeJS (Debian)                      | `fission/node-env-debian` |
| Perl                                 | `fission/perl-env`        |
| PHP 7                                | `fission/php-env`         |
| Python 3                             | `fission/python-env`      |
| Ruby                                 | `fission/ruby-env`        |

To create custom environments you can extend one of the environments in the list or create your own environment from scratch.
