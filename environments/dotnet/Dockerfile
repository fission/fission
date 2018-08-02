FROM microsoft/dotnet:1.1-sdk AS builder

COPY * /proj/
RUN cd /proj && ./project-build.sh

# Build env image
FROM microsoft/dotnet:1.1.0-runtime

WORKDIR /fission-workdir
COPY --from=builder /proj/out .
EXPOSE 8888

ENTRYPOINT ["dotnet"]

CMD ["fission-dotnet.dll"]
