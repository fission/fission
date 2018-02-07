#!/bin/sh
dotnet restore fission-dotnet.csproj
dotnet publish fission-dotnet.csproj -c Release -o out
