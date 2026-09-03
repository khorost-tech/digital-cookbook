"""Ось взаимного рукопожатия: обе стороны одной строкой.

Сервер требует сертификат и от клиента. Ломается КЛИЕНТСКИЙ сертификат —
и вопрос не в том, откажет ли сервер (откажет), а в том, что об этом
узнает клиент.

Поэтому строка несёт обе стороны сразу: что сказал клиент и что записал
сервер. Собирать только клиентскую сторону бессмысленно — утверждать об
асимметрии было бы нечем.

ПОЧЕМУ КЛИЕНТ ПРИСЫЛАЕТ СВЯЗКУ, А НЕ ОДИН ЛИСТ. Без пути к корню сервер
не построит цепочку, и ЛЮБОЙ слом объяснялся бы одинаково — «неизвестный
центр». Просроченный клиентский сертификат отличался бы от чужого двумя
признаками сразу, и ось мерила бы не то.

ОЖИДАНИЯ ЗАПИСАНЫ ЗДЕСЬ, ДО ПРОГОНА:
  mutual_valid     — соединился (контроль);
  mutual_none      — отказ; клиент не прислал сертификата;
  mutual_expired   — отказ; срок клиентского сертификата истёк;
  mutual_other_ca  — отказ; клиентский сертификат от чужого центра.

Отдельно ожидается главное: у клиента все три отказа будут выглядеть
ОДИНАКОВО, а у сервера — по-разному. Уверенность средняя; проверяется
подсчётом различимых причин с каждой стороны, а не на глаз.
"""

import io
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import clients
from scripts.outcome import parse_line

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE_PORT = 18640

# (имя случая, связка клиента, ключ клиента, ожидание)
PROBES = [
    ("mutual_valid", "pki/client-valid.pem", "pki/valid.key", "connected"),
    ("mutual_none", "", "", "rejected"),
    ("mutual_expired", "pki/client-expired.pem", "pki/expired.key", "rejected"),
    ("mutual_other_ca", "pki/client-other.pem", "pki/other-ca.key", "rejected"),
]

SERVER_FLAGS = ["--require-client-cert", "--client-ca=root.pem"]


def main():
    rows = []
    port = BASE_PORT
    for case, cert, key, want in PROBES:
        for client in clients.CLIENTS:
            with clients.Server("valid", port, extra=SERVER_FLAGS) as server:
                line = clients.run_client(client, case, port, kind="mutual",
                                          client_cert=cert, client_key=key)
                row = parse_line(line)
                # Серверная сторона дописывается к той же строке: две
                # половины одного события должны лежать вместе, иначе их
                # рано или поздно сопоставят неверно.
                row["server_saw"] = server.saw()
            port += 1
            mark = "" if row["outcome"] == want else "  <- РАСХОЖДЕНИЕ"
            sys.stderr.write("%-16s %-8s клиент: %-10s сервер: %s%s\n"
                             % (case, client, row["outcome"],
                                row["server_saw"][:60], mark))
            rows.append(row)

    out = os.path.join(HERE, "fixtures/mutual.jsonl")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    import json
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
        handle.write("COMPLETE\n")
    print("записано %d строк в %s" % (len(rows), out))


if __name__ == "__main__":
    main()
