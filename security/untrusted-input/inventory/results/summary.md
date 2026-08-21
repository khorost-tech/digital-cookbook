# Кто видит FFmpeg внутри образа

Окружение прогона: `results/environment.txt`. Сырые артефакты: `results/<цель>/`.

## 1. Инвентаризация: виден ли компонент

| Цель | Способ поставки | FFmpeg физически в образе | syft: найденные компоненты FFmpeg | тип записи | всего компонентов |
|---|---|---|---|---|---|
| `distro` | пакет дистрибутива (apt) | да | ffmpeg@7:5.1.9-0+deb12u1, libavcodec59@7:5.1.9-0+deb12u1, libavdevice59@7:5.1.9-0+deb12u1, libavfilter8@7:5.1.9-0+deb12u1, libavformat59@7:5.1.9-0+deb12u1, libavutil57@7:5.1.9-0+deb12u1 | deb | 288 |
| `vendored` | бинарник мимо пакетного менеджера | да | ffmpeg@8.1 | binary | 29 |
| `jellyfin` | вендорская сборка jellyfin-ffmpeg | да | jellyfin-ffmpeg7@7.1.3-6-trixie | deb | 264 |
| `deb-canonical` | КОНТРОЛЬ: тот же бинарник, deb с именем ffmpeg | да | ffmpeg@8.1 | deb | 93 |
| `deb-renamed` | КОНТРОЛЬ: тот же бинарник, deb с именем acmecorp-ffmpeg7 | да | acmecorp-ffmpeg7@8.1 | deb | 93 |

## 2. Сопоставление: связан ли компонент с уязвимостями

«Находки» — записи матчера, а не уникальные уязвимости: один идентификатор
может дать несколько записей по разным пакетам. Поэтому отдельно приведено
число уникальных CVE/GHSA, отнесённых к компоненту FFmpeg.

| Цель | grype: находки / по FFmpeg / уник. ID по FFmpeg | trivy: находки / по FFmpeg / уник. ID по FFmpeg | osv: находки / по FFmpeg |
|---|---|---|---|
| `distro` | 657 / **168** / 28 | 663 / **168** / 28 | 662 / **252** |
| `vendored` | 13 / **7** / 7 | 0 / **0** / 0 | 0 / **0** |
| `jellyfin` | 380 / **0** / 0 | 393 / **0** / 0 | 384 / **0** |
| `deb-canonical` | 174 / **65** / 65 | 83 / **41** / 41 | 197 / **75** |
| `deb-renamed` | 109 / **0** / 0 | 42 / **0** / 0 | 122 / **0** |

### Отнесён ли CVE-2026-8461 к компоненту FFmpeg

Проверка структурная: идентификатор уязвимости и имя пакета берутся из одной
записи. Поиск строки по всему JSON этого не доказывал бы — CVE мог бы
упоминаться в связанных данных другого пакета.

| Цель | grype: к каким пакетам отнесён | trivy: к каким пакетам отнесён |
|---|---|---|
| `distro` | ffmpeg@7:5.1.9-0+deb12u1, libavcodec59@7:5.1.9-0+deb12u1, libavdevice59@7:5.1.9-0+deb12u1, libavfilter8@7:5.1.9-0+deb12u1, libavformat59@7:5.1.9-0+deb12u1, libavutil57@7:5.1.9-0+deb12u1, libpostproc56@7:5.1.9-0+deb12u1, libswresample4@7:5.1.9-0+deb12u1, libswscale6@7:5.1.9-0+deb12u1 | ffmpeg@7:5.1.9-0+deb12u1, libavcodec59@7:5.1.9-0+deb12u1, libavdevice59@7:5.1.9-0+deb12u1, libavfilter8@7:5.1.9-0+deb12u1, libavformat59@7:5.1.9-0+deb12u1, libavutil57@7:5.1.9-0+deb12u1, libpostproc56@7:5.1.9-0+deb12u1, libswresample4@7:5.1.9-0+deb12u1, libswscale6@7:5.1.9-0+deb12u1 |
| `vendored` | ffmpeg@8.1 | — |
| `jellyfin` | — | — |
| `deb-canonical` | ffmpeg@8.1 | — |
| `deb-renamed` | — | — |

## 3. Чем компонент представлен в SBOM

Идентичность компонента для сопоставления складывается из имени, версии,
типа пакета, namespace дистрибутива и purl/CPE — а не из содержимого файла.

| Цель | name | version | type | purl |
|---|---|---|---|---|
| `distro` | ffmpeg | 7:5.1.9-0+deb12u1 | deb | `pkg:deb/debian/ffmpeg@7%3A5.1.9-0%2Bdeb12u1?arch=amd64&distro=debian-12.15` |
| `vendored` | ffmpeg | 8.1 | binary | `pkg:generic/ffmpeg@8.1` |
| `jellyfin` | jellyfin-ffmpeg7 | 7.1.3-6-trixie | deb | `pkg:deb/debian/jellyfin-ffmpeg7@7.1.3-6-trixie?arch=amd64&distro=debian-13.5&upstream=jellyfin-ffmpeg%407.1.3-6` |
| `deb-canonical` | ffmpeg | 8.1 | deb | `pkg:deb/ubuntu/ffmpeg@8.1?arch=amd64&distro=ubuntu-24.04` |
| `deb-renamed` | acmecorp-ffmpeg7 | 8.1 | deb | `pkg:deb/ubuntu/acmecorp-ffmpeg7@8.1?arch=amd64&distro=ubuntu-24.04` |

## Как читать

- «FFmpeg физически в образе = да» проверено перед сканированием (запуск/наличие
  бинарника). Если сканер при этом молчит — это слепое пятно инструмента, а не
  ошибка эксперимента.
- Колонка «находки» показывает, что инструмент отработал и образ увидел: дело не в
  сломанном прогоне, а в том, что конкретный компонент не попал в его картину мира.
