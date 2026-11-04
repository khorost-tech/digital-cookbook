# -*- coding: utf-8 -*-
"""Закрепление внешних версий — проверяется, а не обещается.

ЗАЧЕМ ЭТОТ ФАЙЛ.

У стенда была честная оговорка: «образы закреплены тегами, а не суммами».
Оговорка верная, и всё же это признание, а не мера. Она объясняет, почему
завтрашний прогон может дать другие числа, и ничего не делает, чтобы он
их не давал. Пока стенд тянул извне подвижные указатели — теги образов,
тег реестра vcpkg, архивы без сумм и pip-пакет с незакреплёнными
зависимостями, — воспроизводимость у него была у́же, чем выглядела: те же
команды завтра собрали бы другой стенд, и фикстуры сравнивались бы с
чужими числами МОЛЧА.

Закрепление сделано; здесь оно стережётся. Без этих проверок первая же
правка Dockerfile'а вернула бы тег на место, и никто бы не заметил —
ровно так протухал прежний маркер прогона.

ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ. Форма, а не значения: что каждый внешний образ
назван дайджестом, что один и тот же образ закреплён везде ОДНИМ
дайджестом, что каждый скачиваемый архив сверяется по сумме ДО
распаковки, что суммы одного архива совпадают во всех файлах, что conan
ставится файлом требований с суммами всех зависимостей и что реестр
vcpkg закреплён суммой коммита.

ЧЕГО ЗДЕСЬ НЕТ. Сверки значений с реестрами: она требовала бы сети и
краснела бы от чужих перебоев, а не от правки стенда. И проверки того,
что сверка сумм ДЕЙСТВИТЕЛЬНО останавливает сборку: строчку `sha256sum
-c` можно написать так, что она ничего не решает. Это не читается
глазами по тексту — это проверяется диверсиями группы machinery
(`pinning-archive-sum-is-connected`, `pinning-wheel-hash-is-connected`),
которые портят сумму и требуют, чтобы сборка встала.

ЧЕГО ЗАКРЕПЛЕНИЕ НЕ ДАЁТ. Герметичности. Внутри образов остаются живые
репозитории дистрибутивов (`apt-get install`, `apk add`); часть пакетов
там закреплена по версии, остальные — нет вовсе. Стенд закрепил то, что
умеет назвать по имени и сумме, и не притворяется, что закрепил всё.
"""

import io
import json
import os
import re
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
IMAGES = os.path.join(HERE, "images")
REQUIREMENTS = os.path.join(IMAGES, "conan-requirements.txt")
BUILD_SPEC = os.path.join(IMAGES, "build-variants.json")

DIGEST = re.compile(r"@sha256:[0-9a-f]{64}(?![0-9a-f])")
SHA256_HEX = re.compile(r"\b[0-9a-f]{64}\b")
COMMIT_SHA = re.compile(r"^[0-9a-f]{40}$")
ARCHIVE_URL = re.compile(r"https?://\S+?\.(?:tar\.gz|tgz|zip)\b")
UNPACK = re.compile(r"\btar\s+-?[a-zA-Z]*x[a-zA-Z]*\b|\bunzip\b")

# Образы, которые стенд собирает сам. Их закреплять нечем и незачем:
# они не приезжают извне, а получаются из закреплённых.
OWN_IMAGE_PREFIX = "cpp-build-and-package-"


def dockerfiles():
    return sorted(name for name in os.listdir(IMAGES)
                  if name.startswith("Dockerfile."))


def read(name):
    with io.open(os.path.join(IMAGES, name), encoding="utf-8") as handle:
        return handle.read()


def instructions(text):
    """Инструкции Dockerfile со склеенными переносами строк.

    Сверка суммы и распаковка стоят в ОДНОЙ инструкции RUN, разорванной
    обратными косыми на десяток строк. Построчный разбор увидел бы их
    как разные команды и не смог бы сказать, что идёт раньше.
    """
    out = []
    current = []
    for line in text.split("\n"):
        stripped = line.strip()
        if not current and (not stripped or stripped.startswith("#")):
            continue
        current.append(stripped[:-1].strip() if stripped.endswith("\\")
                       else stripped)
        if not stripped.endswith("\\"):
            out.append(" ".join(current))
            current = []
    if current:
        out.append(" ".join(current))
    return out


def base_images():
    """Все образы из строк FROM: (файл, образ)."""
    out = []
    for name in dockerfiles():
        for instruction in instructions(read(name)):
            if not instruction.upper().startswith("FROM "):
                continue
            image = instruction.split()[1]
            out.append((name, image))
    return out


def external_base_images():
    return [(name, image) for name, image in base_images()
            if image != "scratch" and not image.startswith(OWN_IMAGE_PREFIX)]


def archive_downloads():
    """Скачивания архивов: (файл, url, инструкция целиком)."""
    out = []
    for name in dockerfiles():
        for instruction in instructions(read(name)):
            for url in ARCHIVE_URL.findall(instruction):
                out.append((name, url, instruction))
    return out


def requirements():
    """Разбор conan-requirements.txt: имя -> (версия, множество сумм)."""
    with io.open(REQUIREMENTS, encoding="utf-8") as handle:
        text = handle.read()
    text = re.sub(r"\\\s*\n", " ", text)
    out = {}
    for line in text.split("\n"):
        line = line.split("#")[0].strip()
        if not line:
            continue
        head = line.split()[0]
        name = re.split(r"[=<>!~\[]", head, 1)[0].strip()
        version = head[len(name):].strip()
        hashes = set(re.findall(r"--hash=sha256:([0-9a-f]{64})", line))
        out[name] = (version, hashes)
    return out


def arm(name):
    with io.open(BUILD_SPEC, encoding="utf-8") as handle:
        spec = json.loads(handle.read())
    for entry in spec["arms"]:
        if entry["name"] == name:
            return entry
    raise AssertionError("нет плеча %r в build-variants.json" % name)


def vcpkg_ref_of(entry):
    args = entry["build_args"]
    for value in args:
        if value.startswith("VCPKG_REF="):
            return value.split("=", 1)[1]
    return None


class BaseImages(unittest.TestCase):
    def test_every_external_base_image_is_pinned_by_digest(self):
        """Тег — подвижный указатель, а не версия.

        Под тем же именем завтра лежит другое содержимое. Прогон,
        сравниваемый с этими фикстурами, сравнивал бы их с ДРУГИМ
        образом и молчал бы об этом.
        """
        unpinned = ["%s: %s" % (name, image)
                    for name, image in external_base_images()
                    if not DIGEST.search(image)]
        self.assertEqual(
            unpinned, [],
            "внешние образы закреплены только тегом: %r. Тег закрепляет "
            "имя, а не содержимое — нужен FROM образ:тег@sha256:…"
            % unpinned)

    def test_the_same_image_is_pinned_to_one_digest_everywhere(self):
        """Один образ — один дайджест во всех файлах.

        gcc:15 стоит в четырёх файлах, alpine:3.22 — в двух. Разойдись
        их дайджесты, плечи собирались бы РАЗНЫМИ компиляторами под
        одним именем, и разница в размере бинарника приписывалась бы
        чему угодно, кроме настоящей причины.
        """
        by_tag = {}
        for name, image in external_base_images():
            tag, digest = image.split("@", 1)
            by_tag.setdefault(tag, {}).setdefault(digest, []).append(name)
        split = {tag: {d: sorted(f) for d, f in digests.items()}
                 for tag, digests in by_tag.items() if len(digests) > 1}
        self.assertEqual(
            split, {},
            "один и тот же тег закреплён разными дайджестами: %r" % split)


class Archives(unittest.TestCase):
    def test_every_downloaded_archive_is_checked_before_it_is_unpacked(self):
        """Сначала сверить, потом распаковать — а не наоборот.

        Раньше архив шёл прямо в tar по конвейеру: распаковывался
        раньше, чем его кто-либо смотрел. Подмена содержимого прошла бы
        молча — и молча попала бы в бинарник, размер которого стенд
        публикует.
        """
        self.assertTrue(archive_downloads(), "скачиваний архивов не нашлось "
                                             "вовсе — проверка ничего не "
                                             "сторожит")
        bad = []
        for name, url, instruction in archive_downloads():
            check = instruction.find("sha256sum -c")
            unpack = UNPACK.search(instruction)
            if check < 0:
                bad.append("%s: %s — суммы нет" % (name, url))
            elif unpack is None:
                bad.append("%s: %s — архив не распаковывается в той же "
                           "инструкции, порядок не прочитать" % (name, url))
            elif check > unpack.start():
                bad.append("%s: %s — сверка суммы стоит ПОСЛЕ распаковки"
                           % (name, url))
        self.assertEqual(bad, [], "архивы берутся без сверки: %r" % bad)

    def test_the_same_archive_carries_the_same_sum_everywhere(self):
        """Один архив — одна сумма во всех файлах.

        Архив fmt скачивают три файла, архив cmake — два. Разойдись
        суммы, один из них молча брал бы другой файл.
        """
        by_url = {}
        for name, url, instruction in archive_downloads():
            sums = set(SHA256_HEX.findall(instruction))
            by_url.setdefault(url, {})[name] = sums
        split = {url: places for url, places in by_url.items()
                 if len({frozenset(s) for s in places.values()}) > 1}
        self.assertEqual(
            split, {},
            "один и тот же архив сверяется разными суммами: %r" % split)


class PipPackage(unittest.TestCase):
    def test_conan_is_installed_from_a_hashed_requirements_file(self):
        """`pip install conan==2.32.0` закрепляет один пакет из шестнадцати.

        Пятнадцать зависимостей приезжали те, что окажутся свежими в
        день сборки, — внутрь ЗАМЕРЯЕМОЙ сборки входил подвижный набор,
        о котором фикстура не говорила ничего.
        """
        text = read("Dockerfile.build-conan")
        joined = " ".join(instructions(text))
        self.assertIn(
            "--require-hashes", joined,
            "conan ставится без --require-hashes: pip примет любое колесо")
        self.assertIn(
            "conan-requirements.txt", joined,
            "в Dockerfile.build-conan нет ссылки на файл требований")
        self.assertIsNone(
            re.search(r"pip3?\s+install(?:\s+--[^\s]+)*\s+conan==", joined),
            "conan всё ещё ставится строкой `pip install conan==…`: "
            "зависимости при этом не закреплены ничем")

    def test_every_requirement_is_pinned_with_a_version_and_a_hash(self):
        """Незакреплённое требование обесценивает весь файл.

        --require-hashes отказывается ставить набор, где закреплено не
        всё, — но отказывается ВО ВРЕМЯ сборки. Здесь то же самое видно
        без Docker и до прогона.
        """
        parsed = requirements()
        self.assertGreater(len(parsed), 1,
                           "в файле требований одно требование — значит "
                           "зависимости conan туда не попали")
        self.assertIn("conan", parsed, "в файле требований нет самого conan")
        bad = ["%s%s" % (name, version)
               for name, (version, hashes) in sorted(parsed.items())
               if not version.startswith("==") or not hashes]
        self.assertEqual(
            bad, [],
            "требования без точной версии или без суммы: %r" % bad)


class VcpkgRegistry(unittest.TestCase):
    """Реестр закрепляется КОММИТОМ, а не тегом.

    Тег git подвижен: его можно перевесить, и клон по тому же имени
    принесёт другое дерево. Microsoft документирует закрепление реестра
    (builtin-baseline) именно как commit SHA:
    https://learn.microsoft.com/en-gb/vcpkg/users/versioning
    """

    def test_the_registry_default_is_a_commit_sha(self):
        text = read("Dockerfile.build-vcpkg")
        match = re.search(r"^ARG VCPKG_REF=(\S+)$", text, re.M)
        self.assertIsNotNone(match, "в Dockerfile.build-vcpkg нет ARG "
                                    "VCPKG_REF")
        self.assertRegex(
            match.group(1), COMMIT_SHA,
            "реестр vcpkg закреплён не суммой коммита, а именем %r: имя "
            "закрепляет указатель, а не дерево" % match.group(1))

    def test_the_measured_arm_takes_the_registry_by_commit(self):
        ref = vcpkg_ref_of(arm("vcpkg"))
        self.assertIsNotNone(ref, "плечо vcpkg не передаёт VCPKG_REF")
        self.assertRegex(
            ref, COMMIT_SHA,
            "измеряемое плечо vcpkg берёт реестр по имени %r, а не по "
            "сумме коммита" % ref)

    def test_the_unpinned_control_stays_unpinned(self):
        """ЭТО ПРОВЕРКА ПРОТИВ СОБСТВЕННОГО ЖЕ ЗАКРЕПЛЕНИЯ.

        Плечо-контроль vcpkg-latest существует ровно затем, чтобы
        показать, чем оборачивается НЕзакреплённый реестр: манифест
        называет зависимость, но не её версию, и версию выбирает
        состояние реестра. Закрепи его «за компанию» — и находка
        исчезнет, а на её месте окажется второе плечо vcpkg, которое
        ничего не показывает. Порядок правок, при котором это случилось
        бы незаметно, вполне обычный: правишь пару, закрепляешь обе
        половины.
        """
        control = arm("vcpkg-latest")
        ref = vcpkg_ref_of(control)
        self.assertIsNotNone(ref, "плечо vcpkg-latest не передаёт VCPKG_REF")
        self.assertNotRegex(
            ref, COMMIT_SHA,
            "контроль vcpkg-latest закреплён коммитом %r — он перестал "
            "быть контролем НЕзакреплённого реестра и стал вторым плечом "
            "vcpkg" % ref)
        self.assertIs(
            control["expected_build_ok"], False,
            "контроль vcpkg-latest объявлен собирающимся — он обязан "
            "падать, иначе он ничего не показывает")

    def test_the_version_is_taken_before_the_compile(self):
        """У упавшего плеча образа не будет — версия обязана сниматься раньше.

        Контроль падает НА КОМПИЛЯЦИИ. Снимай версию после неё — у
        контроля она осталась бы пустой, и утверждение «незакреплённый
        реестр подсовывает fmt такой-то версии» опиралось бы на память о
        пробе, а не на фикстуру.
        """
        steps = instructions(read("Dockerfile.build-vcpkg"))
        measure = [i for i, s in enumerate(steps) if "fmt-version.sh" in s]
        compile_ = [i for i, s in enumerate(steps) if "cmake -S /src" in s]
        self.assertEqual(len(measure), 1, "шаг снятия версии не один")
        self.assertEqual(len(compile_), 1, "шаг компиляции сервиса не один")
        self.assertLess(
            measure[0], compile_[0],
            "версия fmt снимается ПОСЛЕ компиляции: у падающего контроля "
            "она осталась бы пустой")


if __name__ == "__main__":
    unittest.main()
