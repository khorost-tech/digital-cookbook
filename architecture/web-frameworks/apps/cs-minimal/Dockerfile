FROM mcr.microsoft.com/dotnet/sdk:10.0.401@sha256:e70cdb7f80b0348f5cb85f19a8f670fca061f033d57eed12fa003d58b0e06317
WORKDIR /src
COPY . .
RUN dotnet publish -c Release -o /out -nologo -v q
ENTRYPOINT ["dotnet", "/out/app.dll"]
