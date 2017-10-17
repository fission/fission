#!/bin/sh
dotnet restore fission-dotnet20.csproj
dotnet publish fission-dotnet20.csproj -c Release -o out
