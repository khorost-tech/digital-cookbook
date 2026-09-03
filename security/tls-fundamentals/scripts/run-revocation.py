"""Проба про отзыв: 2×2, где меняется ровно одно.

  сертификат    список отозванных   ожидаемый исход
  отозванный    не дан              соединился
  отозванный    дан                 отказал
  контрольный   не дан              соединился
  контрольный   дан                 соединился   <- ключевая клетка

Последняя строка — тот самый контроль, без которого вывод был бы ложным.
Без неё «со списком отказал» объяснялось бы двумя причинами сразу: то ли
сертификат отозван, то ли проверка отзыва вообще ничего не пропускает.
Здесь это не выдумка: первая попытка передать список падала на неизвестной
опции, openssl выходил с ненулевым кодом, и «отказ» получал в том числе
НЕотозванный сертификат. Матрица выглядела осмысленной и была неверной.

ПОЧЕМУ ТОЛЬКО OPENSSL. Список отозванных передаётся клиенту по-разному, и
у трёх остальных это либо отдельная настройка среды, либо код. Проба
показывает не «кто проверяет отзыв», а другое: ОДИН И ТОТ ЖЕ сертификат
получает разный ответ в зависимости от того, дали ли клиенту состояние,
которого в сертификате нет. Для этого хватает одного клиента, и
расширять вывод на остальных нельзя.
"""

import io
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import clients
from scripts.outcome import parse_line

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE_PORT = 18610

# (случай, давать ли список, ожидание — записано ДО прогона)
PROBES = [
    ("revoked", False, "connected"),
    ("revoked", True, "rejected"),
    ("valid", False, "connected"),
    ("valid", True, "connected"),
]


def main():
    lines = []
    for index, (case, with_crl, want) in enumerate(PROBES):
        # Метка строки несёт признак списка. Иначе две строки про
        # отозванный сертификат были бы неразличимы в фикстуре, а
        # различие между ними и есть весь смысл пробы.
        label = case + ("_with_crl" if with_crl else "")
        with clients.Server(case, BASE_PORT + index):
            line = clients.run_client(
                "openssl", label, BASE_PORT + index, kind="revocation",
                crl="pki/crl.pem" if with_crl else "")

        row = parse_line(line)
        tag = "со списком" if with_crl else "без списка"
        sys.stderr.write("%-8s %-11s -> %-10s (ждали %s) %s\n"
                         % (case, tag, row["outcome"], want,
                            "" if row["outcome"] == want else "<- РАСХОЖДЕНИЕ"))
        lines.append(line)

    out = os.path.join(HERE, "fixtures/revocation.jsonl")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(lines) + "\nCOMPLETE\n")
    print("записано %d строк в %s" % (len(lines), out))


if __name__ == "__main__":
    main()
