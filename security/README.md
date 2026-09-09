# Security — примеры

Авторизация и готовый IdP, недоверенный ввод, аудит уязвимостей и изоляция декодеров.

| Стенд | Описание | Статья |
|---|---|---|
| [`keycloak/`](keycloak/) | Keycloak как готовый IdP: realms и clients, потоки аутентификации, деплой и интеграция сервисов | [статья](https://khorost.tech/security/keycloak-when-to-use-idp/) |
| [`pixelsmash/`](pixelsmash/) | CVE-2026-8461 в декодерах FFmpeg: оборонительный стенд — аудит-скрипты и песочница для декодирования недоверенного видео | [статья](https://khorost.tech/security/pixelsmash-ffmpeg/) |
| [`tls-fundamentals/`](tls-fundamentals/) | Фундамент TLS: матрица сломов цепочки доверия на четырёх клиентах, взаимное рукопожатие, имя сервера открытым текстом до шифрования, метка о понижении версии, число сообщений в 1.2 против 1.3 | [статья](https://khorost.tech/security/tls-fundamentals/) |
| [`untrusted-input/`](untrusted-input/) | Недоверенный ввод: инвентаризация источников, обнаружение тихого повреждения данных и трёхролевой конвейер обработки файлов с изоляцией | [статья](https://khorost.tech/security/vendored-dependencies-blind-spot/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
