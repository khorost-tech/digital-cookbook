"""Какую долю времени занимает сама база — и остаётся ли что измерять.

ЗАЧЕМ ЭТА ПРОВЕРКА, А НЕ ТА, ЧТО В python/web. Там нагрузчик работал в
несколько процессов, и вопрос стоял «хватает ли ему мощности насытить
цель»; ответом была полка по числу процессов. Здесь замер последовательный
и в одно соединение — пропускной способности базы он не касается вовсе, и
полка ничего бы не доказала.

Настоящий риск этой волны другой. Цена слоя доступа — это работа в
процессе Python. Время базы — это её собственная работа плюс путь до неё.
Если второе заглушает первое, разница между сырым драйвером и ORM утонет,
и стенд выдаст «все слои одинаковы»: правдоподобно и бесполезно. Ровно так
в волне 2 цена фреймворка утонула в двадцати миллисекундах ожидания.

Поэтому проверяется доля: сколько от клиентского времени приходится на
работу сервера. Серверное время берётся из pg_stat_statements — это то,
что база потратила на выполнение, без пути до неё и без работы клиента.

Вывод не «годен/негоден», а КАРТА РАЗРЕШЕНИЯ: на каком запросе цену слоя
измерять можно, а на каком нельзя. Запрос, где сервер съедает почти всё,
из сравнения слоёв не выбрасывается — он в нём и нужен, чтобы показать,
где разница перестаёт существовать. Но интерпретировать его как «слои
одинаковы по цене» нельзя, и об этом должна знать статья.
"""

import json
import os
import sys

import psycopg

sys.path.insert(0, "/app")
sys.path.insert(0, "/bench")

import arms  # noqa: E402
import common  # noqa: E402
import runner  # noqa: E402

# Порог, выше которого клетка не годится для сравнения слоёв: почти всё
# время ушло серверу, и разница между плечами лежит в оставшемся хвосте.
SERVER_SHARE_LIMIT = 0.80


def server_ms(conn, needle):
    """Среднее время выполнения на сервере по pg_stat_statements, мс."""
    with conn.cursor() as cur:
        cur.execute(
            "SELECT sum(total_exec_time) / nullif(sum(calls), 0) "
            "FROM pg_stat_statements WHERE query LIKE %s",
            (f"%{needle}%",))
        row = cur.fetchone()
    return float(row[0]) if row and row[0] is not None else None


def reset(conn):
    with conn.cursor() as cur:
        cur.execute("SELECT pg_stat_statements_reset()")
    conn.commit()


def main():
    dsn = os.environ["DSN"]
    problems = []
    print("=" * 78)
    print("КАРТА РАЗРЕШЕНИЯ: сколько времени занимает сама база")
    print("=" * 78)
    print()
    print(f"  {'запрос':<12}{'клиент, мс':>14}{'сервер, мс':>14}"
          f"{'доля сервера':>16}")

    rows_out = []
    for query in common.SQL:
        # Обнуление ровно перед измеряемой пачкой, а не до прогрева: иначе
        # сервер считает и прогревочные вызовы, а клиент — нет, и доля
        # сервера выходит больше ста процентов.
        def _reset():
            with psycopg.connect(dsn) as conn:
                reset(conn)

        # Мерим САМОЕ ДЕШЁВОЕ плечо: у него клиентская часть минимальна, и
        # доля сервера получается максимально возможной. Если даже здесь
        # серверу достаётся не всё, то у более дорогих плеч тем более.
        row = runner.run(arms.RawAsyncpg, query, before_measure=_reset)

        with psycopg.connect(dsn) as conn:
            # Опознавательный кусок SQL для каждого запроса. У
            # heavy-entities в тексте есть и books, и reviews, поэтому
            # общий признак «FROM books» отобрал бы лишнее.
            needle = {"one-row": "WHERE id =",
                      "many-rows": "FROM books ORDER BY",
                      "heavy-sql": "FROM reviews r JOIN",
                      "heavy-rows": "HAVING avg",
                      "heavy-entities": "HAVING avg"}[query]
            srv = server_ms(conn, needle)

        client = row["mean_ms"]
        share = (srv / client) if (srv and client) else None
        rows_out.append({"query": query, "client_ms": client,
                         "server_ms": srv, "server_share": share})
        share_txt = f"{share:.0%}" if share is not None else "—"
        print(f"  {query:<12}{client:>14.4f}{(srv or 0):>14.4f}"
              f"{share_txt:>16}")
        if share is not None and share > SERVER_SHARE_LIMIT:
            problems.append(query)

    print()
    if problems:
        print("  Для сравнения слоёв НЕПРИГОДНЫ: " + ", ".join(problems))
        print("  На этих запросах почти всё время принадлежит серверу, и")
        print("  разница между плечами лежит в том, что осталось. Такой")
        print("  запрос показывает, ГДЕ цена слоя перестаёт существовать, но")
        print("  не даёт мерить её саму.")
    else:
        print("  Все запросы годятся для сравнения слоёв: серверу")
        print(f"  принадлежит меньше {SERVER_SHARE_LIMIT:.0%} клиентского времени.")

    print()
    print(json.dumps({"resolution_map": rows_out,
                      "server_share_limit": SERVER_SHARE_LIMIT},
                     ensure_ascii=False, sort_keys=True))
    # Код возврата не «негоден»: карта разрешения — это описание стенда, а
    # не приговор ему. Отказ здесь означал бы, что запрос heavy-sql нельзя
    # ставить, тогда как он поставлен ровно затем, чтобы показать границу.
    return 0


if __name__ == "__main__":
    sys.exit(main())
