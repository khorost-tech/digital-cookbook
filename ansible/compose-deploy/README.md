# Ansible: деплой Docker Compose через community.docker

Рабочий стенд к статье [«Деплой Docker Compose через Ansible»](https://khorost.tech/infrastructure/ansible-docker-compose-deploy/) на khorost.tech.

Стенд показывает полный цикл деплоя стека Docker Compose с помощью Ansible:
параметризацию через `group_vars`, рендер шаблонов `docker-compose.yml` и `.env`,
управление секретами через `ansible-vault`, подъём стека модулем `community.docker.docker_compose_v2`.

Демо-стек: **Caddy** (reverse-proxy) перед **traefik/whoami** (эхо-сервис).

---

## Содержание

1. [Проверено на](#проверено-на)
2. [Требования](#требования)
3. [Запуск](#запуск)
4. [Структура](#структура)
5. [Секреты и ansible-vault](#секреты-и-ansible-vault)
6. [Troubleshooting](#troubleshooting)
7. [Очистка](#очистка)

---

## Проверено на

| Компонент | Версия |
|-----------|--------|
| Ansible core | 2.20.3 (минимум 2.15) |
| Коллекция `community.docker` | 5.0.6 |
| Docker Engine | 26.x+ |
| Docker Compose plugin | v2.27+ (`docker compose`) |
| ОС целевого хоста | Ubuntu 24.04 LTS / Debian 12 |

---

## Требования

- Ansible >= 2.15 (рекомендуется 2.17)
- Коллекция `community.docker`:
  ```bash
  ansible-galaxy collection install community.docker
  ```
- Docker Engine + Compose plugin на целевом хосте
- Python 3.x на целевом хосте (для Ansible-модулей)

Для локального запуска на dev (localhost) Docker должен быть доступен текущему пользователю
без `sudo` (группа `docker`):

```bash
sudo usermod -aG docker $USER
# Перелогиниться или: newgrp docker
```

---

## Запуск

### 1. Установить зависимости Ansible

```bash
ansible-galaxy collection install community.docker
```

### 2. Запустить деплой на dev (localhost)

```bash
cd ansible/compose-deploy
ansible-playbook -i inventory/hosts.ini deploy.yml -l dev
```

> Inventory указываем флагом `-i` явно: так команда не зависит от того, подхватился ли
> `ansible.cfg` (Ansible игнорирует его, например, на world-writable путях).

После успешного деплоя сервис доступен на `http://localhost:8080`.
Ответ whoami покажет HTTP-заголовки запроса — это подтверждает, что Caddy проксирует трафик.

### 3. Запустить на stage

Раскомментируйте в `inventory/hosts.ini` строку `stage-server` с реальным IP,
настройте SSH-доступ, затем:

```bash
ansible-playbook -i inventory/hosts.ini deploy.yml -l stage
```

### 4. Запустить на prod (с секретами)

```bash
# Подготовить secrets.yml (один раз):
cp group_vars/prod/secrets.example.yml group_vars/prod/secrets.yml
$EDITOR group_vars/prod/secrets.yml   # вписать реальный APP_ADMIN_TOKEN
ansible-vault encrypt group_vars/prod/secrets.yml

# Деплой с расшифровкой vault:
ansible-playbook -i inventory/hosts.ini deploy.yml -l prod --ask-vault-pass
```

### Проверка синтаксиса (без подключения к хостам)

```bash
ansible-playbook -i inventory/hosts.ini deploy.yml --syntax-check
```

### Dry-run с просмотром изменений

```bash
ansible-playbook -i inventory/hosts.ini deploy.yml --check --diff -l dev
```

> **Примечание:** в `--check`-режиме шаг `docker_compose_v2` может завершиться с ошибкой,
> если Docker не установлен или `deploy_dir` не существует — это ожидаемо.
> Шаблоны Jinja2 при этом всё равно рендерятся и проверяются.

---

## Структура

```
ansible/compose-deploy/
├── ansible.cfg                        # Конфигурация Ansible (inventory, host_key_checking)
├── deploy.yml                         # Основной плейбук
├── .gitignore                         # Исключает secrets.yml из git
│
├── inventory/
│   └── hosts.ini                      # Группы: dev, stage, prod
│
├── group_vars/
│   ├── all.yml                        # Общие переменные для всех окружений
│   ├── dev.yml                        # Переменные dev (localhost, 1 реплика)
│   ├── stage.yml                      # Переменные stage
│   ├── prod.yml                       # Переменные prod (2 реплики)
│   └── prod/
│       └── secrets.example.yml        # Шаблон секретов → скопировать в secrets.yml
│
└── roles/
    └── compose_stack/
        ├── defaults/main.yml          # Дефолтные значения переменных роли
        ├── tasks/main.yml             # Задачи: mkdir, template, docker_compose_v2
        └── templates/
            ├── docker-compose.yml.j2  # Шаблон Compose-файла (Caddy + whoami)
            └── env.j2                 # Шаблон .env-файла
```

### Переменные

| Переменная | Дефолт (all.yml) | Описание |
|------------|------------------|----------|
| `stack_name` | `compose-demo` | Имя стека (используется в путях) |
| `deploy_dir` | `/opt/stacks/compose-demo` | Каталог деплоя на хосте. В dev переопределён на `~/stacks/...` (поднимается без root); prod использует `/opt/stacks` и включает `ansible_become` |
| `compose_project` | `compose-demo` | Имя проекта Docker Compose |
| `app_image` | `traefik/whoami:v1.10` | Образ приложения (пинуется по тегу) |
| `proxy_image` | `caddy:2.8-alpine` | Образ reverse-proxy |
| `app_port` | `8080` | Внешний порт (маппится на 80 внутри) |
| `app_domain` | `localhost` | Домен для виртуального хоста |
| `app_replicas` | `1` | Количество реплик приложения |
| `env_name` | `dev` | Имя окружения (dev/stage/prod) |
| `app_admin_token` | — | **Секрет.** Только из `secrets.yml` (vault) |

---

## Секреты и ansible-vault

Секреты (токены, пароли, API-ключи) не хранятся в открытом виде в репозитории.
Схема работы:

```
group_vars/prod/secrets.example.yml   ← в git (шаблон с REPLACE-ME)
group_vars/prod/secrets.yml           ← НЕ в git (реальные значения, зашифровано vault)
```

**Создание зашифрованного файла:**

```bash
cp group_vars/prod/secrets.example.yml group_vars/prod/secrets.yml
$EDITOR group_vars/prod/secrets.yml
ansible-vault encrypt group_vars/prod/secrets.yml
```

**Изменение значения без полной расшифровки:**

```bash
ansible-vault edit group_vars/prod/secrets.yml
```

**Передача пароля vault через файл (для CI):**

```bash
echo "vault_password_here" > ~/.vault_pass
chmod 600 ~/.vault_pass
ansible-playbook deploy.yml -l prod --vault-password-file ~/.vault_pass
```

---

## Troubleshooting

| Симптом | Причина и решение |
|---------|-------------------|
| `community.docker.docker_compose_v2` не найден | Коллекция не установлена: `ansible-galaxy collection install community.docker` |
| `Permission denied` при создании `/opt/stacks/` | Нужен root. prod уже включает `ansible_become: true`; dev использует каталог в `$HOME` без root. Для своих хостов в системных путях добавьте `ansible_become`/`become` или используйте каталог в `$HOME` |
| `docker compose` не найден на хосте | Установлен только старый `docker-compose` (v1). Установите Docker Compose plugin v2: `apt install docker-compose-plugin` |
| Порт 8080 уже занят | Измените `app_port` в `group_vars/dev.yml` на свободный порт |
| `--check` завершается с ошибкой на шаге `docker_compose_v2` | В check-режиме Ansible не создаёт `deploy_dir`, поэтому модуль не может найти директорию. Это ожидаемо — шаблоны при этом всё равно рендерятся |
| `APP_ADMIN_TOKEN` не определён | Переменная `app_admin_token` не задана. Для dev/stage — добавьте в `group_vars/dev.yml`. Для prod — создайте `secrets.yml` через ansible-vault |

---

## Очистка

```bash
# Остановить и удалить стек (без удаления volumes):
ansible -l dev -m community.docker.docker_compose_v2 \
  -a "project_src={{ deploy_dir }} project_name={{ compose_project }} state=absent"

# Или напрямую на хосте:
cd /opt/stacks/compose-demo
docker compose down

# Удалить с volumes:
docker compose down -v
```

---

Статья: [khorost.tech/infrastructure/ansible-docker-compose-deploy/](https://khorost.tech/infrastructure/ansible-docker-compose-deploy/)
