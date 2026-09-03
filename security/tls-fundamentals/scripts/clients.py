"""Запуск сервера стенда и четырёх клиентов поверх него.

Здесь одно место, где известно, как позвать каждого клиента. Разборы
осей зовут отсюда, а не повторяют команды: иначе оси незаметно
разъедутся в настройках, и расхождение исходов будет объясняться не
клиентами, а нашими командами.

ПОЧЕМУ СЕРВЕР ЖДУТ ПО СТРОКЕ, А НЕ ПО ТАЙМЕРУ. Ожидание по таймеру
давало бы плавающие прогоны: на загруженной машине сервер не успевает,
и клиент получает отказ соединения, неотличимый в таблице от отказа по
существу. Сервер сам печатает READY.

ПОЧЕМУ ПОРТ ПРОВЕРЯЕТСЯ НА ЗАНЯТОСТЬ. Забытый сервер прошлого случая
держит порт, клиент говорит со СТАРЫМ сервером и возвращает зелёный
результат. Такое уже случилось в этом стенде: ложный успех выглядел
ровно как настоящий контрольный проход.
"""

import os
import shutil
import socket
import threading
import subprocess
import sys
import time

CLIENTS = ("openssl", "curl", "go", "java")

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _server_binary():
    for name in ("server/server.exe", "server/server"):
        path = os.path.join(HERE, name)
        if os.path.exists(path):
            return path
    raise RuntimeError("сервер не собран: нет server/server[.exe]")


def _port_is_free(port):
    probe = socket.socket()
    try:
        probe.connect(("127.0.0.1", port))
    except OSError:
        return True
    finally:
        probe.close()
    return False


def _shell_path():
    """PATH, в котором у оболочки есть её собственные инструменты.

    Оболочка, запущенная из другого процесса, наследует PATH этого
    процесса — и на Windows в нём нет ни grep, ни sed. Скрипты-клиенты
    тогда работали наполовину и молчали об этом. Сейчас об отсутствии
    инструмента они говорят вслух, но правильнее просто дать им PATH,
    в котором инструменты есть.
    """
    path = os.environ.get("PATH", "")
    shell = shutil.which("bash")
    if not shell:
        return path
    root = os.path.dirname(os.path.dirname(os.path.abspath(shell)))
    for extra in (os.path.join(root, "usr", "bin"), os.path.dirname(shell)):
        if os.path.isdir(extra) and extra not in path:
            path = extra + os.pathsep + path
    return path


class Server(object):
    """Сервер стенда, поднятый на одном случае.

    После строки готовности поток ошибок продолжает читаться в отдельной
    нити и складывается в saw(). Там лежит то, что увидел СЕРВЕР, — а это
    отдельная величина: при сломе клиентского сертификата причина отказа
    остаётся у сервера, клиенту достаётся общее сообщение о сбое
    рукопожатия. Не собирая серверную сторону, утверждать об этом нечем.
    """

    def __init__(self, case, port, extra=()):
        self.case = case
        self.port = port
        self.extra = list(extra)
        self.proc = None
        self._saw = []
        self._reader = None

    def _pump(self):
        for line in self.proc.stderr:
            line = line.strip()
            if line.startswith("SERVER_SAW "):
                self._saw.append(line[len("SERVER_SAW "):])

    def saw(self):
        """Что сервер сказал о последнем рукопожатии."""
        return self._saw[-1] if self._saw else ""

    def __enter__(self):
        if not _port_is_free(self.port):
            raise RuntimeError(
                "порт %d занят: прошлый сервер не завершился, и клиент "
                "заговорит с ним, а не с нашим" % self.port)
        cmd = [_server_binary(), "--case=" + self.case, "--pki=pki",
               "--addr=0.0.0.0:%d" % self.port] + self.extra
        self.proc = subprocess.Popen(
            cmd, cwd=HERE, stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE, text=True, encoding="utf-8",
            errors="replace")
        deadline = time.time() + 20
        while time.time() < deadline:
            line = self.proc.stderr.readline()
            if not line:
                break
            if line.startswith("READY"):
                self._reader = threading.Thread(target=self._pump, daemon=True)
                self._reader.start()
                return self
        self.__exit__(None, None, None)
        raise RuntimeError("сервер не сообщил о готовности на случае %r"
                           % self.case)

    def __exit__(self, *exc):
        if self.proc is not None:
            self.proc.kill()
            self.proc.wait()
            if self.proc.stderr is not None:
                self.proc.stderr.close()
        return False


def go_probe():
    """Путь к собранному клиенту на Go.

    Имя зависит от того, чем собирали: сводный прогон делает `probe`, а
    ручная сборка на Windows часто даёт `probe.exe`. Знание об этом
    держится ЗДЕСЬ и больше нигде — тест уже один раз искал только
    `probe.exe` и падал под сводным прогоном.
    """
    for name in ("clients/go/probe.exe", "clients/go/probe"):
        path = os.path.join(HERE, name)
        if os.path.exists(path):
            return path
    raise RuntimeError("клиент на Go не собран: нет clients/go/probe[.exe]")


def run_client(client, case, port, kind="chain", servername="stand.local",
               ca="pki/root.pem", client_cert="", client_key="", crl="", max_version=""):
    """Зовёт одного клиента и возвращает его строку как есть.

    Строка НЕ разбирается здесь: разбор — дело contract-модуля
    scripts.outcome, и он должен видеть ровно то, что клиент напечатал.
    """
    addr = "127.0.0.1:%d" % port
    env = dict(os.environ, MSYS_NO_PATHCONV="1")
    env["PATH"] = _shell_path()

    flags = ["--kind=" + kind, "--case=" + case, "--addr=" + addr,
             "--servername=" + servername, "--ca=" + ca]
    if crl:
        flags.append("--crl=" + crl)
    if max_version:
        flags.append("--max-version=" + max_version)
    if client_cert:
        flags += ["--client-cert=" + client_cert, "--client-key=" + client_key]

    if client == "go":
        cmd = [go_probe()] + flags
    elif client in ("openssl", "curl", "java"):
        # Путь относительный: команда исполняется из корня стенда, а
        # абсолютный путь Windows в оболочке съедает обратные слэши.
        # Корень стенда не передаётся: скрипт берёт его из текущего
        # каталога, а команда и так исполняется из корня. Путь Windows,
        # прошедший через оболочку, в docker не годится ни с обратными
        # слэшами (их съедает экранирование), ни с прямыми (том с буквой
        # диска в таком виде не принимается).
        cmd = ["bash", "clients/%s.sh" % client] + flags
    else:
        raise ValueError("неизвестный клиент %r" % client)

    done = subprocess.run(cmd, cwd=HERE, env=env, capture_output=True,
                          text=True, encoding="utf-8", errors="replace")
    out = done.stdout.strip()
    if not out:
        # Клиент не сказал ничего — это сбой пробы, и он обязан быть
        # виден, а не исчезнуть из таблицы.
        raise RuntimeError("клиент %s ничего не напечатал на случае %r:\n%s"
                           % (client, case, done.stderr.strip()))
    return out.split("\n")[-1]


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18447
    with Server("valid", port):
        for name in CLIENTS:
            print(run_client(name, "valid", port))
