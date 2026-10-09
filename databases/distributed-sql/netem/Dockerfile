# Инструмент для задержек между узлами: запускается в сетевом пространстве
# узла (--net container:<узел>) и вешает netem только на трафик к соседям.
FROM alpine:3.20
RUN apk add --no-cache iproute2 iputils
