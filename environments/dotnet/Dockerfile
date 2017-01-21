FROM microsoft/dotnet:1.1.0-runtime

WORKDIR /fission-workdir
COPY out .
EXPOSE 8888

ENTRYPOINT ["dotnet"]

CMD ["fission-dotnet.dll"]