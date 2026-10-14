#!/bin/sh
# Проверка прав py-spy. Печатает факты, не мнения.
cd /bench
echo "пользователь: $(id -un) (uid=$(id -u))"
echo "CapEff: $(grep CapEff /proc/self/status | awk '{print $2}')"
if [ -r /proc/sys/kernel/yama/ptrace_scope ]; then
  echo "Yama ptrace_scope: $(cat /proc/sys/kernel/yama/ptrace_scope)"
else
  echo "Yama ptrace_scope: модуль не включён (файла нет)"
fi

echo "--- режим 1: py-spy САМ запускает цель ---"
/opt/py/gil/bin/py-spy record --format raw --rate 50 --duration 2 --output /tmp/r.raw -- \
  /opt/py/gil/bin/python -c 'from workload import cpu_task
for _ in range(400): cpu_task(25000)' 2>&1 | grep -E "Samples|Error|error" | head -2

echo "--- режим 2: присоединение к СОСЕДНЕМУ процессу (не потомку py-spy) ---"
/opt/py/gil/bin/python -c 'from workload import cpu_task
while True: cpu_task(25000)' &
target=$!
sleep 2
echo "  цель pid=$target, её родитель pid=$$ (та же оболочка, py-spy ей не предок)"
/opt/py/gil/bin/py-spy dump --pid "$target" 2>&1 | grep -E "^Process|^Error|Python v" | head -2
kill "$target" 2>/dev/null
wait "$target" 2>/dev/null

echo "--- режим 3: присоединение ОТ ДРУГОГО пользователя к процессу root ---"
/opt/py/gil/bin/python -c 'from workload import cpu_task
while True: cpu_task(25000)' &
t2=$!
sleep 2
su nobody -s /bin/sh -c "/opt/py/gil/bin/py-spy dump --pid $t2" 2>&1 | grep -E "^Process|Permission|^Error" | head -2
kill "$t2" 2>/dev/null
