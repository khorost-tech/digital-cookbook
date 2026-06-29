# docker/rootless — rootful vs rootless на живом стенде

Минимальный стенд к статье [«Rootless Docker: стоит ли переходить»](https://khorost.tech/docker/rootless-docker/).
Один крошечный Go-сервис (`probe`) наглядно показывает главное практическое
различие между rootful и rootless Docker — **кому на хосте принадлежат файлы,
созданные контейнером в смонтированном volume**.

## Что демонстрирует

`probe` при старте:

1. пишет файл `data/written-by-container.txt` в bind-mount `/data`;
2. отдаёт по `GET /info` свой `uid`/`gid` **внутри** контейнера.

Контейнер намеренно работает от root внутри (без `USER`). Интересен владелец
файла **снаружи**, на хосте:

| Режим демона | `uid_inside` (внутри) | Владелец файла на хосте | Удалить файл |
|---|---|---|---|
| rootful | 0 (root) | root | нужен `sudo` |
| rootless | 0 (root) | ваш пользователь | без `sudo` |

Тот же контейнер, тот же код — разный владелец на хосте. В rootless «root»
внутри контейнера маппится на ваш непривилегированный uid (через `subuid`),
поэтому контейнер больше не плодит root-owned файлы в ваших каталогах.

## Запуск

Требуется Docker (rootful или rootless) и `docker compose`.

> **Важно: запускайте на нативной Linux-ФС** (например, `$HOME/...` на ext4),
> а **не** на WSL-mount (`/mnt/c`, `/mnt/g`), DrvFs или NFS с root squash. Такие
> ФС маскируют владельца файлов: даже на rootful-демоне файл в bind mount получит
> ваш uid вместо root, и наблюдение ownership станет недостоверным. `smoke.sh`
> это распознаёт и предупреждает, но честную демонстрацию даёт только Linux-ФС.

```bash
# полная демонстрация: поднять, показать uid внутри и владельца на хосте, погасить
bash scripts/smoke.sh
```

Скрипт сам определит режим демона (`rootless: true/false`) и пояснит, что вы
видите. После работы стенд гасится, каталог `data/` очищается (trap EXIT).

Ручной вариант:

```bash
mkdir -p data
docker compose up -d --build
curl -s http://localhost:8080/info     # uid ВНУТРИ контейнера
ls -ln data/                           # владелец файла на ХОСТЕ — главное наблюдение
docker compose down -v && rm -rf data
```

## Бонус: привилегированные порты

```bash
bash scripts/port-80-demo.sh
```

Пробует опубликовать сервис на `:80`. В rootful — работает, в rootless —
отказ, пока не понижен `net.ipv4.ip_unprivileged_port_start` (подробности —
в [статье](https://khorost.tech/docker/rootless-docker/#limitations)). Поэтому
в `docker-compose.yml` сервис намеренно опубликован на `:8080`.

## Структура

```
docker/rootless/
  docker-compose.yml        # probe + bind mount + порт 8080
  probe/
    main.go                 # Go-сервис: пишет в /data, отдаёт uid по /info
    Dockerfile              # multi-stage, статический бинарник
    go.mod
  scripts/
    smoke.sh                # сквозная демонстрация (up → наблюдение → down)
    port-80-demo.sh         # демонстрация ограничения портов <1024
```

## Лицензия

MIT — см. [LICENSE](../../LICENSE) в корне репозитория.
