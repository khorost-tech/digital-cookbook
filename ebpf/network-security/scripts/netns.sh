#!/bin/sh
# Две пространства имён, соединённые veth: snd (адреса 10.99.0.1–4) и rcv (10.99.0.100).
# Пересоздаются при каждом вызове — счётчики iptables и XDP начинаются с нуля.
set -e
ip netns del snd 2>/dev/null || true
ip netns del rcv 2>/dev/null || true
ip netns add snd
ip netns add rcv
ip link add veth-snd netns snd type veth peer name veth-rcv netns rcv
for i in 1 2 3 4; do ip -n snd addr add 10.99.0.$i/24 dev veth-snd; done
ip -n rcv addr add 10.99.0.100/24 dev veth-rcv
ip -n snd link set veth-snd up
ip -n rcv link set veth-rcv up
ip -n snd link set lo up
ip -n rcv link set lo up
