dotnet restore
dotnet publish -c Release -o out
docker build -t fission/dotnet20-env .
