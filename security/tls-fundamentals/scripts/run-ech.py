"""Счётный факт: сколько из четырёх клиентов умеют шифрованное приветствие.

Ось «что видно до шифрования» доказывает, что имя сервера едет открытым
текстом. Стандартный ответ на это — шифровать само приветствие клиента.
Статья, которая ставит проблему и молчит об ответе, неполна; но и
рассуждать о зрелости решения без чисел незачем.

ЧТО ЗДЕСЬ МЕРИТСЯ И ЧТО НЕТ.

Мерится ровно одно: УМЕЕТ ли клиент. Исход целый — умеет или нет.

НЕ мерится, работает ли это на практике: для настоящего шифрованного
приветствия нужны записи в системе имён и поддержка на стороне сервера,
и ни того ни другого стенд не поднимает. Об этом сказано прямо, а не
обойдено молчанием.

ПОЧЕМУ ПРОВЕРКА — ДЕЙСТВИЕМ, А НЕ ЧТЕНИЕМ СПРАВКИ.

У curl флаг для шифрованного приветствия ЕСТЬ, а возможности нет:
попытка им воспользоваться отвечает «установленная библиотека этого не
поддерживает». Считать по наличию флага значило бы переоценить
поддержку ровно на одного клиента из четырёх. Поэтому там, где можно,
проверка пробует применить, а не читает описание.
"""

import io
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

OPENSSL_IMAGE = "tls-stand-openssl:1"
CURL_IMAGE = "curlimages/curl:8.18.0"
JAVA_IMAGE = "eclipse-temurin:25-jdk"

ENV = dict(os.environ, MSYS_NO_PATHCONV="1")


# Семья реализации TLS. Клиентов четыре, но независимых семей меньше:
# openssl и curl работают на одной библиотеке, просто разных её версий.
#
# Правило намеренно простое и проверяемое по строке версии: если в ней
# упомянут OpenSSL — значит OpenSSL, чем бы клиент ни назывался.
STACKS = (
    ("openssl", "OpenSSL"),
    ("go version", "Go crypto/tls"),
    ("java", "Java JSSE"),
)


def tls_stack(version):
    """Какая библиотека делает TLS у этого клиента."""
    low = version.lower()
    for marker, name in STACKS:
        if marker in low:
            return name
    return "неизвестно"


def stack_version(version):
    """Версия именно TLS-библиотеки, а не программы вокруг неё."""
    import re
    found = re.search(r"OpenSSL[ /](\d+\.\d+\.\d+)", version)
    if found:
        return "OpenSSL " + found.group(1)
    found = re.search(r"go(\d+\.\d+(?:\.\d+)?)", version)
    if found:
        return "Go " + found.group(1)
    found = re.search(r"Java ([\d.]+)", version)
    if found:
        return "Java " + found.group(1)
    return version


def run(cmd):
    done = subprocess.run(cmd, capture_output=True, text=True,
                          encoding="utf-8", errors="replace", env=ENV)
    return (done.stdout + done.stderr).strip()


def check_openssl():
    """Есть ли у клиента ключ для шифрованного приветствия."""
    out = run(["docker", "run", "--rm", OPENSSL_IMAGE, "s_client", "-help"])
    version = run(["docker", "run", "--rm", OPENSSL_IMAGE, "version"])
    has = any(line.strip().startswith("-ech")
              for line in out.split("\n"))
    return {
        "client": "openssl",
        "version": version.split("\n")[0],
        "supported": has,
        "how": "s_client -help: искали ключ, начинающийся с -ech",
        "evidence": "ключ найден" if has else "ключа нет",
    }


def check_curl():
    """Флаг есть — а работает ли он.

    Ровно тот случай, ради которого проверка делается действием.
    """
    version = run(["docker", "run", "--rm", "--entrypoint", "curl",
                   CURL_IMAGE, "--version"]).split("\n")[0]
    features = run(["docker", "run", "--rm", "--entrypoint", "curl",
                    CURL_IMAGE, "--version"])
    tried = run(["docker", "run", "--rm", "--entrypoint", "curl", CURL_IMAGE,
                 "--ech", "true", "--max-time", "3",
                 "https://example.invalid/"])
    flag_exists = "option --ech" in tried or "--ech" not in tried
    works = "does not support this" not in tried
    return {
        "client": "curl",
        "version": version,
        "supported": works,
        "how": "попытка воспользоваться флагом --ech",
        "evidence": tried.split("\n")[0][:120],
        "note": ("флаг в справке есть, в списке возможностей ECH нет — "
                 "поэтому проверка пробует применить, а не читает справку"
                 if not works else ""),
        "features_line": [l for l in features.split("\n")
                          if l.startswith("Features:")][:1],
    }


def check_go():
    """Есть ли в настройках поля про шифрованное приветствие."""
    probe = os.path.join(HERE, "fixtures/.echcheck")
    os.makedirs(probe, exist_ok=True)
    with io.open(os.path.join(probe, "main.go"), "w",
                 encoding="utf-8", newline="\n") as handle:
        handle.write(
            "package main\n\n"
            'import (\n\t"crypto/tls"\n\t"fmt"\n\t"reflect"\n\t"strings"\n)\n\n'
            "func main() {\n"
            "\tt := reflect.TypeOf(tls.Config{})\n"
            "\tvar found []string\n"
            "\tfor i := 0; i < t.NumField(); i++ {\n"
            "\t\tn := t.Field(i).Name\n"
            '\t\tif strings.Contains(n, "EncryptedClientHello") {\n'
            "\t\t\tfound = append(found, n)\n"
            "\t\t}\n"
            "\t}\n"
            '\tfmt.Println(strings.Join(found, ","))\n'
            "}\n")
    with io.open(os.path.join(probe, "go.mod"), "w",
                 encoding="utf-8", newline="\n") as handle:
        handle.write("module echcheck\n\ngo 1.25\n")
    out = subprocess.run(["go", "run", "."], cwd=probe, capture_output=True,
                         text=True, encoding="utf-8", errors="replace",
                         env=ENV).stdout
    version = run(["go", "version"])
    fields = [f for f in out.strip().split(",") if f]
    return {
        "client": "go",
        "version": version,
        "supported": bool(fields),
        "how": "поля tls.Config с именем EncryptedClientHello*",
        "evidence": ", ".join(fields) if fields else "полей нет",
    }


def check_java():
    """Есть ли в настройках соединения методы про шифрованное приветствие."""
    probe = os.path.join(HERE, "fixtures/.echcheck-java")
    os.makedirs(probe, exist_ok=True)
    with io.open(os.path.join(probe, "EchCheck.java"), "w",
                 encoding="utf-8", newline="\n") as handle:
        handle.write(
            "import javax.net.ssl.SSLParameters;\n"
            "import java.lang.reflect.Method;\n"
            "import java.util.ArrayList;\n"
            "import java.util.List;\n\n"
            "public class EchCheck {\n"
            "    public static void main(String[] args) {\n"
            "        List<String> found = new ArrayList<>();\n"
            "        for (Method m : SSLParameters.class.getMethods()) {\n"
            "            String n = m.getName().toLowerCase();\n"
            '            if (n.contains("ech") '
            '|| n.contains("encryptedclienthello")) {\n'
            "                found.add(m.getName());\n"
            "            }\n"
            "        }\n"
            '        System.out.println(System.getProperty("java.version")'
            ' + "|" + String.join(",", found));\n'
            "    }\n"
            "}\n")
    out = run(["docker", "run", "--rm", "-v", probe.replace(os.sep, "/") + ":/w",
               "-w", "/w", JAVA_IMAGE, "java", "EchCheck.java"])
    version, _, methods = out.strip().partition("|")
    found = [m for m in methods.split(",") if m]
    return {
        "client": "java",
        "version": "Java " + version,
        "supported": bool(found),
        "how": "методы SSLParameters с упоминанием шифрованного приветствия",
        "evidence": ", ".join(found) if found else "методов нет",
    }


def main():
    rows = [check_openssl(), check_curl(), check_go(), check_java()]
    for row in rows:
        row["kind"] = "ech"
        # Семья реализации: клиентов четыре, семей меньше.
        row["tls_stack"] = tls_stack(row["version"])
        row["tls_stack_version"] = stack_version(row["version"])
        sys.stderr.write("%-8s %-28s умеет: %s  (%s)\n"
                         % (row["client"], row["version"][:28],
                            "да" if row["supported"] else "нет",
                            row["evidence"][:60]))

    supported = sum(1 for r in rows if r["supported"])
    sys.stderr.write("\nумеют %d из %d\n" % (supported, len(rows)))

    families = {}
    for row in rows:
        families.setdefault(row["tls_stack"], []).append(row["client"])
    sys.stderr.write("семей реализаций: %d при %d клиентах\n"
                     % (len(families), len(rows)))
    for name, clients_of in sorted(families.items()):
        sys.stderr.write("  %-16s %s\n" % (name, ", ".join(clients_of)))

    out = os.path.join(HERE, "fixtures/ech.jsonl")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with io.open(out, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
        handle.write("COMPLETE\n")
    print("записано %d строк в %s" % (len(rows), out))


if __name__ == "__main__":
    main()
