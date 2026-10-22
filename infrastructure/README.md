# Infrastructure — примеры

Инфраструктура как код и деплой: Ansible, Terraform, Proxmox.

| Стенд | Описание | Статья |
|---|---|---|
| [`ansible/`](ansible/) | Деплой Docker Compose стека через Ansible: docker_compose_v2, vault | [статья](https://khorost.tech/infrastructure/ansible-docker-compose-deploy/) |
| [`cicd/`](cicd/) | CI-пайплайн (кэш, reusable workflows, matrix, OIDC), zero-downtime миграции (expand-contract, блокирующий DDL) и release engineering (semver/changelog, DORA) | [статья](https://khorost.tech/infrastructure/ci-pipeline-github-actions/) |
| [`proxmox/`](proxmox/) | Terraform для Proxmox: VM/LXC, cloud-init, for_each | [статья](https://khorost.tech/infrastructure/proxmox-terraform-vm-automation/) |
| [`terraform/`](terraform/) | Стык Terraform → Ansible: docker-хост + firewall + inventory | [статья](https://khorost.tech/infrastructure/terraform-docker-hosts-and-networks/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
