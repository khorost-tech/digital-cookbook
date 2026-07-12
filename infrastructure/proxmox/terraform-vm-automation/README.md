# Примеры: Proxmox + Terraform

Рабочие примеры к статье [«Proxmox + Terraform: автоматизируем создание VM и контейнеров»](https://khorost.tech/infrastructure/proxmox-terraform-vm-automation/) на khorost.tech.

Один набор конфигурации создаёт VM и LXC-контейнеры в Proxmox VE двумя способами на выбор:

- **`import`** (по умолчанию) — Terraform сам скачивает cloud image и импортирует его как диск VM. Шаблон готовить не нужно, всё в коде.
- **`clone`** — Terraform клонирует заранее подготовленный VM-шаблон.

Способ переключается одной переменной `vm_source`.

---

## Содержание

1. [Проверено на](#проверено-на)
2. [Требования](#требования)
3. [Подготовка Proxmox](#подготовка-proxmox)
4. [Запуск](#запуск)
5. [Структура](#структура)
6. [Troubleshooting](#troubleshooting)
7. [Очистка](#очистка)

---

## Проверено на

| Компонент | Версия |
|-----------|--------|
| Terraform | 1.15.x (минимум 1.6) |
| OpenTofu | 1.12.x |
| Провайдер `bpg/proxmox` | 0.111.0 |
| Proxmox VE | 9.2 |
| Образ VM | Ubuntu 26.04 LTS (resolute) |
| Шаблон LXC | Debian 13 (`debian-13-standard_13.1-2`) |

## Требования

- Terraform >= 1.6 (или OpenTofu >= 1.12)
- Доступ к Proxmox VE по API
- SSH-ключ для cloud-init

---

## Подготовка Proxmox

### 1. API-токен (нужен всегда)

```bash
# Пользователь для Terraform
pveum user add terraform@pam

# Роль Administrator на корневой уровень
pveum aclmod / -user terraform@pam -role Administrator

# API-токен без privilege separation (наследует права пользователя)
pveum user token add terraform@pam tf-token --privsep 0
# Сохраните выведенный секрет — повторно его не покажут
```

### 2a. Для `vm_source = "import"` — включить content type `import`

По умолчанию storage не принимает образы для импорта. Разрешаем тип контента `import`
на storage, куда Terraform скачает образ (здесь — `local`):

```bash
pvesm set local --content iso,vztmpl,backup,import
# То же в UI: Datacenter → Storage → local → Edit → Content → отметить «Import»
```

### 2b. Для `vm_source = "clone"` — подготовить VM-шаблон

```bash
cd /var/lib/vz/template/iso/
wget https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img

qm create 9000 --name "ubuntu-cloud-template" --ostype l26 \
  --memory 1024 --cores 1 --cpu host --net0 virtio,bridge=vmbr0
qm importdisk 9000 resolute-server-cloudimg-amd64.img local-lvm
qm set 9000 --scsihw virtio-scsi-single --scsi0 local-lvm:vm-9000-disk-0,ssd=1
qm set 9000 --ide2 local-lvm:cloudinit        # cloud-init drive — обязателен
qm set 9000 --boot order=scsi0
qm set 9000 --serial0 socket --vga serial0
qm template 9000
```

ID шаблона (`9000`) указывается переменной `vm_template_id`.

### 3. LXC-шаблон (для контейнеров)

```bash
pveam update
# Точное имя свежего шаблона — pveam available --section system | grep debian-13
pveam download local debian-13-standard_13.1-2_amd64.tar.zst
```

---

## Запуск

```bash
# Копируем пример переменных и правим под себя
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars   # токен, нода, SSH-ключ, vm_source, карта vms/containers

terraform init
terraform plan
terraform apply

# Проверяем доступ (IP — из output vm_ips)
ssh ops@10.10.10.101
```

Переключение способа создания VM — без правки `.tf`-файлов, только в `terraform.tfvars`:

```hcl
vm_source = "import"   # или "clone"
```

---

## Структура

| Файл | Назначение |
|------|------------|
| `provider.tf` | Провайдер `bpg/proxmox`, подключение к API |
| `variables.tf` | Все входные переменные |
| `vm-clone.tf` | VM из клона шаблона (активен при `vm_source = "clone"`) |
| `vm-import.tf` | Загрузка образа + VM через `import_from` (активен при `vm_source = "import"`) |
| `ct.tf` | LXC-контейнеры |
| `outputs.tf` | IP-адреса созданных VM и контейнеров |
| `terraform.tfvars.example` | Шаблон переменных (скопировать в `terraform.tfvars`) |

Оба `vm-*.tf` объявляют ресурсы через `for_each`, который при неподходящем
`vm_source` сворачивается в пустую карту — активным остаётся ровно один способ.

---

## Troubleshooting

| Симптом | Причина и решение |
|---------|-------------------|
| `apply` падает на загрузке образа (`import`) | На storage не включён content type `import` — см. [шаг 2a](#2a-для-vm_source--import--включить-content-type-import) |
| VM создаётся, но недоступна по SSH | В шаблоне (`clone`) забыт cloud-init drive (`--ide2 local-lvm:cloudinit`) |
| `apply` падает при уменьшении `disk` | Proxmox не уменьшает диски. Увеличьте значение обратно или пересоздайте VM |
| Две VM с одним IP | Дубль `ip` в карте `vms` — Terraform создаст обе, конфликт всплывёт в сети |
| `plan` показывает расхождение после правок в UI | Drift: не меняйте руками то, что управляется Terraform, либо `terraform import` |

---

## Очистка

```bash
# Удалить весь стенд
terraform destroy

# Удалить одну VM
terraform destroy -target='proxmox_virtual_environment_vm.import["k3s-node"]'
```
