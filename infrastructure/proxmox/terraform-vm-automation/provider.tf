# Провайдер bpg/proxmox: VM, LXC, storage, cloud-init.
# Нижняя граница версии — текущий релиз провайдера на момент написания.
terraform {
  required_version = ">= 1.6"

  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.111.0"
    }
  }
}

provider "proxmox" {
  # Базовый URL без /api2/json — провайдер допишет сам. Пример: https://pve:8006
  endpoint  = var.proxmox_url
  api_token = "${var.proxmox_token_id}=${var.proxmox_token_secret}"
  insecure  = true # self-signed сертификаты в homelab

  ssh {
    agent = true
  }
}
