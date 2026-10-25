#!/bin/sh
# run.sh server|client … — выбрать главный класс по первому аргументу.
case "$1" in
  server) shift; exec java -cp /grpc-stand.jar tech.khorost.grpc.Server "$@" ;;
  client) shift; exec java -cp /grpc-stand.jar tech.khorost.grpc.Client "$@" ;;
  *) echo "run.sh server|client …" >&2; exit 2 ;;
esac
