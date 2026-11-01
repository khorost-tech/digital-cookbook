#!/bin/sh
# debugfs и tracefs в VM Docker Desktop не смонтированы: без них нет tracepoint'ов и kprobe через tracefs.
mountpoint -q /sys/kernel/debug || mount -t debugfs none /sys/kernel/debug
mountpoint -q /sys/kernel/tracing || mount -t tracefs none /sys/kernel/tracing
exec "$@"
