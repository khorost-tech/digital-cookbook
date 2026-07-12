# Terraform → Ansible: handoff для docker-хостов

Пример живого стыка слоёв инфраструктуры:

- **Terraform** поднимает VM-docker-хост в Proxmox и firewall-периметр вокруг него,
  затем **генерирует Ansible inventory** из своих выходных данных.
- **Ansible** подхватывает сгенерированный inventory и готовит хост (Docker Engine).
- **Docker Compose** запускает приложения (полный пример — `../../ansible/compose-deploy`).

Статья: https://khorost.tech/infrastructure/terraform-docker-hosts-and-networks/

## Проверено на

- Terraform >= 1.6, провайдер bpg/proxmox >= 0.111.0
- Proxmox VE 8.x
- Ansible (community) с доступом по SSH к хостам

## Структура

```
docker-host-handoff/
├── terraform/        # хост + firewall + генерация inventory
└── ansible/          # тонкий слой: ping + установка Docker
```

## Запуск

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars   # заполнить значения
terraform init
terraform apply                                 # создаёт VM, firewall, рендерит ../ansible/inventory/hosts.ini

cd ../ansible
ansible-playbook -i inventory/hosts.ini site.yml
```

## Очистка

```bash
cd terraform
terraform destroy
```

## Что где живёт (источник истины)

| Сущность | Владелец |
|----------|----------|
| VM-хост, диск, сеть VM | Terraform |
| Firewall-периметр (security group) | Terraform |
| Ansible inventory | Terraform (генерируется) |
| Docker Engine на хосте | Ansible |
| Контейнеры, docker-сети, compose-стек | Docker Compose (через Ansible) |
