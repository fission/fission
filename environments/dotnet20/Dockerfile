FROM microsoft/dotnet:2.0-runtime

WORKDIR /fission-workdir
COPY out .
EXPOSE 8888

ENTRYPOINT ["dotnet"]

CMD ["fission-dotnet20.dll"]