"""Копия cpp/_common/outcome.py — родовой контракт строки фикстуры.

Своя копия, а не импорт: в публичный digital-cookbook стенд уезжает каталогом
data-engineering/dimensional целиком, и cpp/_common там не окажется. Описание формы
контракта и история его усиления — в исходном файле.
"""

import io
import json


class Contract:
    """Контракт строки для одного стенда: свои исходы, общая форма.

    Пример использования в `scripts/outcome.py` стенда::

        from cpp._common.outcome import Contract

        _contract = Contract(outcomes=("served", "refused", "error"),
                              success="served")
        OUTCOMES = _contract.OUTCOMES
        SUCCESS = _contract.SUCCESS
        END_MARKER = _contract.END_MARKER
        parse_line = _contract.parse_line
        read_fixture = _contract.read_fixture
        read_measurements = _contract.read_measurements
    """

    def __init__(self, outcomes, success, fields=None, named_fields=None,
                 end_marker="COMPLETE"):
        if success not in outcomes:
            raise ValueError(
                "успешный исход %r не входит в список исходов %r"
                % (success, outcomes))
        self.OUTCOMES = tuple(outcomes)
        self.SUCCESS = success
        self.FIELDS = tuple(fields or
                             ("kind", "variant", "probe", "outcome", "detail"))
        self.NAMED_FIELDS = tuple(named_fields or ("kind", "variant", "probe"))
        self.END_MARKER = end_marker

    def _require_names(self, row, fields):
        """Требует непустых значений у полей, которые что-то называют.

        Пустое значение здесь ничем не лучше отсутствующего: весь смысл
        требования к имени варианта в том, чтобы два варианта не
        склеились в одну строку фикстуры, а пустое имя склеивает ровно
        так же — только молча, и проверка на одно лишь наличие поля
        этого не видит.
        """
        for field in fields:
            if field not in row:
                raise ValueError("нет обязательного поля %s" % field)
            if not str(row[field]).strip():
                raise ValueError("поле %s пусто" % field)

    def parse_line(self, line):
        """Разбирает одну строку с исходом, проверяя контракт целиком.

        Неуспешный исход обязан объяснять себя. Отказ без объяснения
        неотличим от отказа по любой другой причине, а ошибка без
        объяснения нечем отделить от поломки стенда, которую надо чинить,
        а не печатать.
        """
        row = json.loads(line)
        for field in self.FIELDS:
            if field not in row:
                raise ValueError("нет обязательного поля %s" % field)
        self._require_names(row, self.NAMED_FIELDS)
        if row["outcome"] not in self.OUTCOMES:
            raise ValueError("недопустимый исход %r" % row["outcome"])
        if row["outcome"] != self.SUCCESS and not str(row["detail"]).strip():
            raise ValueError("исход %r обязан нести объяснение"
                              % row["outcome"])
        return row

    def _has_end_marker(self, text):
        """Маркер завершения обязан быть В КОНЦЕ файла, а не где угодно.

        Маркер, случайно оказавшийся внутри значения какого-нибудь поля
        (например, в detail), не считается: иначе файл из одной оборванной
        строки данных выглядел бы как полный прогон.
        """
        lines = [l for l in text.split("\n") if l.strip()]
        return bool(lines) and lines[-1].strip() == self.END_MARKER

    def _read_rows(self, path):
        """Общая часть обоих чтений: маркер, строки данных, запрет пустоты.

        Оборванная фикстура — прогон, упавший посередине. Разбирать её
        нельзя: получится частичная картина, выглядящая как полная. Пустой
        результат (маркер есть, а строк данных нет) — тоже ошибка: проверка,
        зелёная при отсутствии предмета проверки, не работает.
        """
        with io.open(path, encoding="utf-8") as handle:
            text = handle.read()
        if not self._has_end_marker(text):
            raise ValueError("%s: нет маркера завершения в конце файла" % path)
        rows = [l for l in text.split("\n") if l.strip().startswith("{")]
        if not rows:
            raise ValueError("%s: нет ни одной строки данных" % path)
        return rows

    def read_fixture(self, path):
        """Читает фикстуру со строками исхода, требуя маркер завершения."""
        return [self.parse_line(line) for line in self._read_rows(path)]

    def read_measurements(self, path, value_fields=("kind", "variant")):
        """Строки без исхода: величины вместо решений.

        Исход бывает не у всего. У пробы на блокировку он есть — ответили
        или нет. У числа соединений исхода нет, есть величина. Требовать
        поле outcome от такой строки значило бы его выдумывать, а ослабить
        ради неё общий контракт — потерять его там, где он нужен.

        Маркер завершения требуется и здесь, и по той же причине.
        """
        rows = [json.loads(line) for line in self._read_rows(path)]
        for row in rows:
            self._require_names(row, value_fields)
        return rows
