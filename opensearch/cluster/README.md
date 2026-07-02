# Ansible: 3-нодный кластер OpenSearch (self-signed TLS)

Рабочий стенд к статье [«OpenSearch: установка кластера через Ansible, авторизация и работа с индексами»](https://khorost.tech/infrastructure/opensearch-cluster-ansible/) на khorost.tech.

Стенд поднимает 3-нодный кластер OpenSearch официальным плейбуком
[`opensearch-project/ansible-playbook`](https://github.com/opensearch-project/ansible-playbook),
настраивает TLS (transport 9300 + REST 9200) и security plugin с ролевым доступом
(`reader` / `writer` / `admin` над индексами `app-logs-*`) — тот же inventory, `group_vars`
и `securityconfig`, что разобраны в статье.

**Отличие от статьи: без Vault.** В статье сертификаты выпускаются из HashiCorp Vault PKI.
Здесь — тот же результат (TLS для transport-mTLS и REST), но без внешней зависимости от
Vault: `certs/gen-self-signed.sh` генерирует demo-CA и сертификаты нод/admin локально,
через `openssl`. Ключи сразу пишутся в PKCS#8 — шаг конвертации PKCS#1 → PKCS#8,
описанный в статье как отдельная ловушка Vault-варианта, здесь не нужен.

---

## Содержание

1. [Демо-credentials — прочитать перед запуском](#демо-credentials)
2. [Требования](#требования)
3. [Структура](#структура)
4. [Запуск](#запуск)
5. [Проверка кластера](#проверка-кластера)
6. [Self-signed vs Vault PKI](#self-signed-vs-vault-pki)
7. [Troubleshooting](#troubleshooting)
8. [Очистка](#очистка)

---

## Демо-credentials

> **Пароли и сертификаты в этом примере — только для локального/тестового стенда.**
> `securityconfig/internal_users.yml` содержит общеизвестный demo-хеш пароля `admin`/`admin`
> (тот же, что OpenSearch кладёт в свою собственную demo-конфигурацию) и плейсхолдеры
> `<BCRYPT_HASH>` для остальных пользователей. `certs/gen-self-signed.sh` генерирует
> самоподписанный CA и сертификаты без пароля на ключах. **Ничего из этого нельзя
> использовать за пределами тестового окружения.** Перед любым использованием ближе к
> реальной нагрузке: сгенерируйте новые пароли через `hash.sh` из дистрибутива OpenSearch,
> замените демо-сертификаты на выпущенные вашим CA/PKI (например, Vault — см. статью).

---

## Требования

- 3 тестовых хоста/VM под управлением Ansible (в inventory — синтетические
  `os-node-1/2/3`; замените на реальные IP/FQDN своих хостов)
- Ansible >= 2.15
- `openssl` на машине, с которой запускается `gen-self-signed.sh`
- Целевые хосты: поддерживаемый upstream-плейбуком дистрибутив (Ubuntu/Debian/RHEL-семейство),
  Python 3 для Ansible-модулей

---

## Структура

```
opensearch/cluster/
├── deploy.yml                          # Точка входа: клонирует upstream-плейбук,
│                                        # устанавливает OpenSearch, раскладывает TLS,
│                                        # применяет securityconfig
├── inventory/
│   └── hosts.ini                       # Группа [os-cluster]: os-node-1/2/3
├── group_vars/
│   └── all.yml                         # Версия OpenSearch, heap, роли нод, пути TLS
├── certs/
│   └── gen-self-signed.sh              # Demo-CA + node/admin сертификаты (PKCS#8, без Vault)
└── securityconfig/
    ├── internal_users.yml              # admin (demo-хеш) + app_reader/app_writer (плейсхолдеры)
    ├── roles.yml                       # reader / writer / admin над app-logs-*
    └── roles_mapping.yml               # Пользователи/backend_roles → роли
```

---

## Запуск

### 1. Сгенерировать demo-сертификаты

```bash
cd opensearch/cluster/certs
./gen-self-signed.sh
cd ..
```

Результат — каталог `certs/out/` с `root-ca.pem`, `node.pem`/`node-key.pem`,
`admin.pem`/`admin-key.pem`. Ключи уже в PKCS#8 (`-----BEGIN PRIVATE KEY-----`),
конвертация не нужна.

### 2. Заполнить inventory реальными хостами

Отредактируйте `inventory/hosts.ini` — замените синтетические `10.0.0.11/12/13` на IP/FQDN
своих 3 тестовых хостов, проверьте `ansible_user` и SSH-доступ.

### 3. Поднять кластер

```bash
ansible-playbook -i inventory/hosts.ini deploy.yml
```

Плейбук по шагам:

1. клонирует `opensearch-project/ansible-playbook` в `.upstream/` (или обновляет, если уже клонирован);
2. запускает upstream `opensearch.yml` с нашим `group_vars/all.yml` — установка пакета,
   роли нод, heap, discovery из inventory;
3. раскладывает self-signed сертификаты из `certs/out/` на все ноды кластера
   и перезапускает сервис;
4. на первой ноде группы применяет `securityconfig/` через `securityadmin.sh`
   (admin-сертификат, `-icl -nhnv`).

### Проверка синтаксиса без подключения к хостам

```bash
ansible-playbook -i inventory/hosts.ini deploy.yml --syntax-check
```

---

## Проверка кластера

```bash
curl -sk -u "admin:admin" "https://os-node-1:9200/_cluster/health?pretty"
```

Ожидаемый ответ на здоровом кластере — `"status": "green"`. Флаг `-k` — потому что
CA самоподписанный и не в системном доверенном хранилище; для строгой проверки цепочки
используйте `--cacert certs/out/root-ca.pem` вместо `-k`.

```bash
curl -sk --cacert certs/out/root-ca.pem -u "admin:admin" \
  "https://os-node-1:9200/_cat/nodes?v"
```

---

## Self-signed vs Vault PKI

| | Этот пример | Статья (прод-путь) |
|---|---|---|
| Источник сертификатов | `certs/gen-self-signed.sh`, локальный demo-CA | HashiCorp Vault PKI, роль `os-pki` |
| Формат ключа | сразу PKCS#8 | PKCS#1 → конвертация в PKCS#8 через handler |
| Ротация | вручную, перезапуском скрипта | автоматическая, per-host джиттер порога |
| Аутентификация к CA | не требуется | Vault AppRole (`role_id`/`secret_id`) |
| Когда уместно | локальный стенд, CI, обучение | постоянная инфраструктура с централизованной PKI |

Оба пути закрывают одну и ту же задачу — TLS для transport (9300, mTLS) и REST (9200) —
и используют один и тот же security plugin с одинаковым `securityconfig/`. Разница только
в том, откуда берётся сертификат.

---

## Troubleshooting

| Симптом | Причина и решение |
|---------|-------------------|
| `securityadmin.sh` падает с ошибкой авторизации | DN admin-сертификата не совпадает с `plugins.security.authcz.admin_dn`. Сверьте вывод `openssl x509 -in certs/out/admin.pem -noout -subject` с `opensearch_admin_dn` в `group_vars/all.yml` |
| Ноды не видят друг друга / кластер не формируется | Проверьте `vm.max_map_count=262144` на всех хостах и что SAN в `node.pem` покрывает реальные IP/hostname (перегенерируйте `gen-self-signed.sh` с актуальными адресами) |
| `curl` не проходит проверку сертификата без `-k` | Ожидаемо для self-signed CA — используйте `--cacert certs/out/root-ca.pem` вместо `-k` |
| `_cluster/health` = `yellow` сразу после поднятия | Реплики ещё не разъехались по нодам — подождите ребалансировки, это не ошибка |
| Нужны реальные пароли вместо demo | Сгенерируйте хеш: `plugins/opensearch-security/tools/hash.sh -p '<пароль>'`, замените в `securityconfig/internal_users.yml`, повторно прогоните `securityadmin.sh` (шаг 4 в deploy.yml) |

---

## Очистка

```bash
# На каждой ноде кластера:
sudo systemctl stop opensearch
sudo rm -rf /etc/opensearch/tls

# Локально — удалить сгенерированные demo-сертификаты и клон upstream-плейбука:
rm -rf certs/out .upstream
```

---

Статья: [khorost.tech/infrastructure/opensearch-cluster-ansible/](https://khorost.tech/infrastructure/opensearch-cluster-ansible/)
