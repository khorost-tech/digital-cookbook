"""Property-based набор: те же правила, но данные придумывает hypothesis.

ЧЕМ ЭТО ОТЛИЧАЕТСЯ ОТ ПРЕДЫДУЩИХ НАБОРОВ. Там четыре строки данных
подобраны так, чтобы задеть известные автору места: одна задача без
статуса, одно название в верхнем регистре. Подобраны — то есть автор
заранее знал, где искать. Здесь данные генерируются, а правило
проверяется против независимого образца на Python.

ОБРАЗЕЦ ОБЯЗАН БЫТЬ НЕЗАВИСИМЫМ. Если ожидание считать тем же запросом,
что и проверяемое значение, тест сойдётся при любой ошибке. Поэтому
образец ниже написан на голом Python и повторяет ПРАВИЛО, а не запрос.

ДВА ПРОГОНА, И ОНИ МЕРЯЮТ РАЗНОЕ.

    без сжатия  сколько примеров понадобилось, чтобы наткнуться на ошибку
    со сжатием  какой минимальный контрпример остаётся после сокращения

Считать примеры на прогоне со сжатием нельзя: после первого падения
hypothesis прогоняет тест ещё десятки раз, сокращая пример, и счётчик
покажет работу сжатия, а не поиска.

БАЗА ПРИМЕРОВ ОТКЛЮЧЕНА (database=None). Иначе hypothesis запомнит
падавший пример и на следующем прогоне подсунет его первым: «нашлось с
первого примера» окажется свойством кэша, а не поиска.
"""

import os

import pytest
import sqlalchemy
from hypothesis import HealthCheck, Phase, given, settings
from hypothesis import strategies as st

import repo as repo_mod

AUTHORS = ("lem", "strugatsky", "clarke")
STATUSES = ("done", "wip", None)

MAX_EXAMPLES = int(os.environ.get("HYP_MAX_EXAMPLES", "100"))
SHRINK = os.environ.get("HYP_SHRINK", "0") == "1"

PHASES = ((Phase.explicit, Phase.reuse, Phase.generate, Phase.target,
           Phase.shrink) if SHRINK else
          (Phase.explicit, Phase.reuse, Phase.generate, Phase.target))

# Счётчик примеров живёт в conftest: его надо записать в файл после
# прогона, а тело теста об этом знать не обязано.
from conftest import EXAMPLES  # noqa: E402


titles = st.text(alphabet=st.characters(
    min_codepoint=65, max_codepoint=122,
    categories=("Lu", "Ll")), min_size=3, max_size=12)


@st.composite
def dataset(draw):
    """Задачи и поисковый запрос к ним.

    Запрос берётся из настоящего названия и случайно меняет регистр:
    иначе регистронезависимость проверялась бы на словах, которых в
    данных нет, и правило было бы истинным по пустоте.
    """
    rows = draw(st.lists(
        st.fixed_dictionaries({
            "author": st.sampled_from(AUTHORS),
            "title": titles,
            "status": st.sampled_from(STATUSES),
        }), min_size=1, max_size=6))
    for i, row in enumerate(rows, start=1):
        row["id"] = i
    source = draw(st.sampled_from([r["title"] for r in rows]))
    start = draw(st.integers(min_value=0, max_value=max(len(source) - 1, 0)))
    length = draw(st.integers(min_value=1, max_value=len(source) - start))
    term = source[start:start + length]
    term = draw(st.sampled_from([term, term.lower(), term.upper()]))
    return rows, term


def expected_unfinished(rows):
    return sorted(r["id"] for r in rows if r["status"] != "done")


def expected_search(rows, term):
    return sorted(r["id"] for r in rows if term.lower() in r["title"].lower())


def expected_per_author(rows):
    out = {}
    for r in rows:
        total, done = out.get(r["author"], (0, 0))
        out[r["author"]] = (total + 1,
                            done + (1 if r["status"] == "done" else 0))
    return [(a, t, d) for a, (t, d) in sorted(out.items())]


@settings(max_examples=MAX_EXAMPLES, deadline=None, database=None,
          phases=PHASES,
          suppress_health_check=[HealthCheck.function_scoped_fixture])
@given(data=dataset())
def test_rules_hold_for_any_dataset(pg_engine, data):
    rows, term = data
    EXAMPLES["n"] += 1

    with pg_engine.connect() as conn:
        trans = conn.begin()
        try:
            repo_mod.meta.create_all(conn)
            conn.execute(sqlalchemy.delete(repo_mod.tasks))
            conn.execute(repo_mod.tasks.insert(), rows)
            repo = repo_mod.TaskRepo(conn)

            assert repo.unfinished() == expected_unfinished(rows)
            assert repo.search(term) == expected_search(rows, term)
            assert repo.per_author() == expected_per_author(rows)
        finally:
            trans.rollback()


@pytest.fixture(scope="session")
def pg_engine(pg_container):
    engine = sqlalchemy.create_engine(pg_container.get_connection_url())
    yield engine
    engine.dispose()
