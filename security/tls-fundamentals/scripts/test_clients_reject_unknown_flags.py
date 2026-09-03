"""Неизвестный флаг обязан ронять клиента, а не игнорироваться.

Параметры раньше передавались через окружение — и до оболочки, запущенной
из другого процесса, не доходили ВООБЩЕ. Прогон отрабатывал, выглядел
осмысленным и мерил не то: вид пробы брался по умолчанию, список
отозванных не применялся.

Теперь всё передаётся флагами. Чтобы опечатка в имени флага не вернула ту
же беду в новом обличье, каждый клиент обязан на незнакомый флаг падать.
"""

import os
import subprocess
import unittest

from scripts import clients

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class UnknownFlags(unittest.TestCase):
    def test_every_client_fails_on_an_unknown_flag(self):
        for name in clients.CLIENTS:
            if name == "go":
                # Имя бинарника знает clients.go_probe и только он:
                # здесь стояло жёсткое probe.exe, и под сводным прогоном,
                # который собирает probe без расширения, тест падал.
                cmd = [clients.go_probe(), "--case=valid", "--nosuchflag=1"]
            else:
                cmd = ["bash", "clients/%s.sh" % name,
                       "--case=valid", "--nosuchflag=1"]
            done = subprocess.run(cmd, cwd=HERE, capture_output=True,
                                  text=True, encoding="utf-8",
                                  errors="replace")
            self.assertNotEqual(
                done.returncode, 0,
                "%s проглотил незнакомый флаг: значит и опечатку в нужном "
                "флаге проглотит, и проба померит не то" % name)


if __name__ == "__main__":
    unittest.main()
