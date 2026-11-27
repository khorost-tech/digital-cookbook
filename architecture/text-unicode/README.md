# Стенд: тексты и Unicode в шести языках

Стенд к статье серии «Поперёк языков» о строках: одна и та же программа на **Go, Rust, Java,
Python, JavaScript (Node) и C#** отвечает на одни и те же вопросы о тексте, и ответы сравниваются.
Плюс PostgreSQL — как длину и уникальность видит база.

Строки заданы в `vectors.json` **списками code points**, а не литералами: ни редактор, ни терминал
не нормализуют их по дороге, и все шесть программ получают ровно одни и те же строки.

## Оси

| ось | вопрос |
|---|---|
| `lengths` | что возвращает встроенная «длина»: байты, code units UTF-16 или code points; сколько графем в эмодзи |
| `graphemes` | сколько графем в नमस्ते — где реализации расходятся |
| `normalization` | café в NFC и NFD: равны ли до и после нормализации; NFKC для лигатур и полноширинных букв |
| `casefold` | преобразование регистра (İ, турецкая I, ß, длина в UTF-8 до и после), нижний регистр, свёртка и коллация как разные операции: Straße/STRASSE, ς/σ, café/cafe |
| `truncation` | наивная обрезка внутри эмодзи; обрезка по графемам — лимит 7 и 6 графем и бюджет в байтах |
| `security` | уникальность имени без нормализации; какой операцией строится ключ и что он склеивает: полноширинные, гомоглиф, Straße, конечная сигма |
| `invalid` | некорректный UTF-8 и одиночный суррогат: ошибка, замена на U+FFFD или байты как есть |
| `database` | `char_length`, `varchar(n)` и уникальный индекс в PostgreSQL |

Гипотезы — в `expected.json` до прогона, пересмотры (`revision_*`) — до сессий, с причиной.
Результат — `fixtures/checks.jsonl`.

## Версии

| компонент | версия |
|---|---|
| Go | 1.27; `golang.org/x/text` v0.42.0, `github.com/rivo/uniseg` v0.4.7 |
| Rust | 1.98.1; `unicode-normalization` 0.1.25, `unicode-segmentation` 1.13.3 |
| Java | Temurin 25.0.4 (`java.text.Normalizer`, `BreakIterator`, `\X`) |
| Python | 3.14.7; `regex` 2026.9.29 (графемы через `\X`) |
| Node | 24.20.0 (`Intl.Segmenter`, `String.prototype.normalize`) |
| C# | .NET SDK 10.0.401 (`StringInfo`, `Rune`, `string.Normalize`) |
| PostgreSQL | 18.6 |

Образы — по дайджесту (`images.json`); версии библиотек — не моложе двух недель на 19.10.2026.

## Запуск

```bash
scripts/run-all.sh                          # все оси, сводка сессии, маркер, тесты, диверсии
python3 scripts/run-checks.py truncation    # одна ось, без записи фикстуры
```

Нужен Docker; первый прогон скачивает образы и зависимости (модули Go, крейты, `regex`).

## Уборка

```bash
docker volume rm text-unicode-gomod text-unicode-cargo text-unicode-target text-unicode-pydeps text-unicode-dotnet
rm -rf out
```
