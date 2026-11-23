"""Присутствует ли засеянный дефект в собранном бинарнике — по дизассемблеру.

Положительный контроль оси 1: где оптимизатор выкинул код с дефектом,
«детектор пропустил» не является свойством детектора. Признак у каждой
программы свой и описан рядом; читается `objdump -d -C` ТОГО ЖЕ бинарника,
который получил детектор (`detect.sh … --disasm`). Первая редакция читала
сборку того же компилятора без санитайзера и ошиблась: UBSan на -O2 оставил
malloc в heap_overflow и поймал запись, а «присутствие» говорило «нет».

Признаки — по вызову, который несёт дефект (malloc, new[], delete), а не по
форме цикла: инструментация меняет форму кода, но не убирает вызовы. Вызов
ищется по имени цели в любой форме: `malloc@plt`, `malloc`,
`__interceptor_trampoline_malloc` (ASan и TSan). Архитектура — arm64.
"""

import re

def _call(name):
    """Вызов функции `name` в любой форме цели: @plt, перехватчик, прямой."""
    return (r"\bbl\s+[0-9a-f]+ <(__interceptor_(trampoline_)?)?%s" % name
            + (r"(@plt)?>" if name.isalnum() else ""))


# программа: (функция, в теле которой ищем, [regex — все обязаны найтись])
SIGNS = {
    # вызов malloc в main остался: блок выделяется, и цикл пишет в него.
    "heap_overflow": ("main", [_call(r"malloc")]),
    # после вызова operator delete в main есть чтение из памяти.
    "use_after_free": ("main", [_call(r"operator delete\(void\*"),
                                _call(r"operator delete\(void\*") + r"[^\n]*\n(?:.*\n)*?.*\bldr\s"]),
    # сложение с INT_MAX - 1 или INT_MAX осталось в коде (UBSan складывает
    # через adds — с флагом переполнения для своей проверки).
    "signed_overflow": ("main", [r"#0x7ffffff[ef]\b", r"\badds?\s+w\d+"]),
    # тело потока делает неатомарное чтение-изменение-запись счётчика:
    # есть str и нет атомарных инструкций. На -O2 цикл сворачивается в
    # counter += 100000 — это всё ещё неатомарные ldr/add/str, гонка на месте.
    "data_race": ("lambda", [r"\bstr\s+w\d+", r"^(?!.*\b(ldadd|ldaxr|stlxr|cas)\w*\b)"]),
    # выделение operator new[] осталось.
    "leak": ("main", [_call(r"operator new\[\]")]),
    # сравнение прочитанного значения с 1 осталось: g++ пишет cmp, clang —
    # subs с тем же смыслом.
    "uninit_read": ("main", [r"(cmp\s+w\d+, #0x1\b|subs\s+w\d+, w\d+, #0x1\b)"]),
    # обнуление key[length + 1] осталось: запись нулевого байта в parse() —
    # прямо из wzr или через регистр, обнулённый перед записью (так пишет
    # сборка libFuzzer с ASan).
    "parser_edge": ("parse", [r"(\bstrb\s+wzr|\bmov\s+(w\d+), wzr\n(?:.*\n){0,8}?.*\bstrb\s+\2,)"]),
}


def function_body(disassembly, name):
    """Текст тел функций, имя которых содержит `name` (lambda — все тела лямбд)."""
    bodies, current, keep = [], [], False
    for line in disassembly.splitlines():
        m = re.match(r"^[0-9a-f]+ <(.+)>:$", line)
        if m:
            if keep:
                bodies.append("\n".join(current))
            label = m.group(1)
            if name == "main":
                keep = label == "main"
            elif name == "lambda":
                # Тело потока: на -O0 — operator() лямбды; на -O2 лямбда
                # встроена в _M_run() обёртки std::thread, а g++ ещё и
                # склеивает два одинаковых тела в одно. clang называет
                # лямбды main::$_0, g++ — main::{lambda()#1}.
                keep = (("{lambda()" in label or "main::$_" in label)
                        and ("operator()" in label or "_M_run()" in label))
            else:
                keep = label.startswith(name + "(")
            current = []
            continue
        if keep:
            current.append(line)
    if keep:
        bodies.append("\n".join(current))
    return "\n".join(bodies)


def defect_present(program, disassembly):
    """True/False по признакам; None — для чистой программы (дефекта нет по построению)."""
    if program == "clean":
        return None
    func, signs = SIGNS[program]
    body = function_body(disassembly, func)
    if not body.strip():
        raise ValueError("%s: функция %s не найдена в дизассемблере" % (program, func))
    for sign in signs:
        if sign.startswith("^(?!"):
            if re.search(r"\b(ldadd|ldaxr|stlxr|cas)\w*\b", body):
                return False
        elif not re.search(sign, body, re.M):
            return False
    return True
