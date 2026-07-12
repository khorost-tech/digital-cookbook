terraform {
  required_version = ">= 1.6"

  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.111.0"
    }
    # local — для генерации Ansible inventory из выходных данных Terraform
    local = {
      source  = "hashicorp/local"
      version = ">= 2.5"
    }
  }
}

provider "proxmox" {
  endpoint  = var.proxmox_url
  api_token = "${var.proxmox_token_id}=${var.proxmox_token_secret}"
  insecure  = true # self-signed сертификаты в homelab

  ssh {
    agent = true
  }
}
