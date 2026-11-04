"""Печатающий скрипт обязан переключить вывод в UTF-8.

НАЙДЕНО ВТОРЫМ ПРОГОНОМ. На хосте Windows консоль живёт в cp1251. Все
четыре оси там снялись, а `analyze-repro.py` упал на печати знака «≠»,
которого в cp1251 нет: UnicodeEncodeError. Сводный прогон честно
остановился на шаге разбора и маркер не поставил — но до второй
платформы этого дефекта не видел никто, потому что в WSL консоль в UTF-8.

Чинить текст было бы неправильно: знак заменился бы словом, а разбор
умер бы на следующем «×» или «→». Чинится поток — и эта проверка следит,
чтобы ни один печатающий скрипт про это не забыл.

Проверка структурная: она смотрит в исходник, а не в поведение. Поведение
воспроизводится только на консоли с узкой кодовой страницей, которой на
машине сборки может не быть, — а проверка, зелёная от отсутствия
предмета, не работает.
"""

import ast
import io
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(HERE, "scripts")

# Скрипты, которые печатают в консоль. Модули-библиотеки (outcome.py,
# build.py, repro.py, siblings.py, docker.py, console.py) сюда не входят:
# они не точка входа и потоки не трогают.
PRINTING = (
    "analyze-package.py", "analyze-build.py", "analyze-repro.py",
    "analyze-siblings.py",
    "run-package.py", "run-link.py", "run-build.py", "run-repro.py",
    "run-siblings.py",
    "run-manifest.py", "run-marker.py", "run-evidence.py", "sabotage.py",
)


def source(name):
    with io.open(os.path.join(SCRIPTS, name), encoding="utf-8") as handle:
        return handle.read()


def calls_force_utf8_at_import(name):
    """Есть ли в модуле ВЫЗОВ force_utf8() на верхнем уровне.

    Раньше проверка искала подстроку «force_utf8()» в тексте файла — и
    диверсия, убравшая вызов, её не роняла: те же буквы остались в
    строке документации того же файла. Проверка была зелена от
    упоминания предмета вместо самого предмета. Теперь читается дерево
    разбора: вызов на верхнем уровне модуля, а не слово в комментарии.
    """
    tree = ast.parse(source(name))
    for node in tree.body:
        if not isinstance(node, ast.Expr):
            continue
        call = node.value
        if isinstance(call, ast.Call) and getattr(
                call.func, "id", None) == "force_utf8":
            return True
    return False


def captured_subprocess_calls_without_encoding(name):
    """Захват вывода подпроцесса без явной кодировки.

    text=True без encoding отдаёт декодирование кодовой странице
    консоли. На cp1251 это UnicodeDecodeError на первом же русском
    слове в выводе — не в консоли, а ВНУТРИ скрипта, который этот вывод
    читает. Найдено на прогоне диверсий с хоста Windows: тридцать
    диверсий отчитались провалом, потому что читатель их вывода умирал
    раньше, чем успевал сравнить текст.
    """
    found = []
    tree = ast.parse(source(name))
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        func = node.func
        called = getattr(func, "attr", None) or getattr(func, "id", None)
        if called not in ("run", "Popen", "check_output"):
            continue
        names = {kw.arg for kw in node.keywords}
        if ("text" in names or "universal_newlines" in names) \
                and "encoding" not in names:
            found.append(node.lineno)
    return found


class ConsoleEncoding(unittest.TestCase):
    def test_every_printing_script_forces_utf8(self):
        for name in PRINTING:
            self.assertTrue(
                calls_force_utf8_at_import(name),
                "%s печатает в консоль, но не переключает поток в UTF-8: "
                "на консоли cp1251 первый же «≠» или «×» уронит его "
                "UnicodeEncodeError" % name)

    def test_the_package_forces_utf8_for_the_test_suite(self):
        """У набора проверок своей точки входа нет — за него это делает
        пакет.

        Сообщения проверок написаны по-русски и содержат «≠» и «×». Если
        набор запускают напрямую (`python3 -m unittest discover -s
        scripts`), PYTHONIOENCODING не выставлен никем: его ставит только
        bench/run-all.sh. На консоли cp1251 проверка падала бы
        UnicodeEncodeError НА ПЕЧАТИ СОБСТВЕННОГО СООБЩЕНИЯ — в том самом
        окружении, ради которого она и написана, — и читатель увидел бы
        не «что не сошлось», а ошибку кодировки.
        """
        self.assertTrue(
            calls_force_utf8_at_import("__init__.py"),
            "scripts/__init__.py не переключает вывод в UTF-8: набор "
            "проверок, запущенный напрямую на консоли cp1251, умрёт на "
            "печати собственного сообщения")

    def test_every_captured_subprocess_names_its_encoding(self):
        """Чтение вывода подпроцесса не отдаётся кодовой странице.

        НАЙДЕНО ПРОГОНОМ ДИВЕРСИЙ С ХОСТА WINDOWS. Тридцать диверсий
        отчитались провалом разом — не потому, что проверки устояли, а
        потому, что скрипт диверсий читал их вывод с text=True без
        кодировки: на cp1251 русский текст сообщения роняет
        UnicodeDecodeError в потоке-читателе, и до сравнения дело не
        доходит. Дефект того же рода, что и печать «≠»: чинится поток,
        а не текст.
        """
        for name in sorted(os.listdir(SCRIPTS)):
            if not name.endswith(".py"):
                continue
            lines = captured_subprocess_calls_without_encoding(name)
            self.assertEqual(
                lines, [],
                "%s: захват вывода подпроцесса без явной кодировки "
                "(строки %r) — на консоли cp1251 чтение русского вывода "
                "умрёт UnicodeDecodeError" % (name, lines))

    def test_the_list_of_printing_scripts_is_complete(self):
        """Новый печатающий скрипт не должен добавиться молча.

        Список выше — это утверждение о составе стенда. Появившийся
        analyze- или run- скрипт, не попавший в список, обошёл бы
        проверку целиком.
        """
        found = {n for n in os.listdir(SCRIPTS)
                 if (n.startswith("analyze-") or n.startswith("run-"))
                 and n.endswith(".py")}
        self.assertEqual(
            found, set(PRINTING) - {"sabotage.py"},
            "состав печатающих скриптов разошёлся со списком проверки")

    def test_force_utf8_survives_a_stream_that_cannot_reconfigure(self):
        """Поток без reconfigure не должен ронять прогон."""
        import sys

        sys.path.insert(0, HERE)
        from scripts.console import force_utf8

        class Dumb(object):
            pass

        saved = sys.stdout, sys.stderr
        sys.stdout, sys.stderr = Dumb(), Dumb()
        try:
            force_utf8()
        finally:
            sys.stdout, sys.stderr = saved


if __name__ == "__main__":
    unittest.main()
