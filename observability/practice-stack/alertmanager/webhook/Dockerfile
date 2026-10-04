FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /alertwebhook .

FROM alpine:3.22
COPY --from=build /alertwebhook /usr/local/bin/alertwebhook
EXPOSE 9099
ENTRYPOINT ["/usr/local/bin/alertwebhook"]
