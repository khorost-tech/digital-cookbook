# Вариант «без клонирования»: Terraform сам качает cloud image и импортирует
# его как диск VM. Активен при vm_source = "import".
# Требует включённого content type "import" на storage cloud_image_datastore.

resource "proxmox_download_file" "cloud_image" {
  count = var.vm_source == "import" ? 1 : 0

  content_type = "import"
  datastore_id = var.cloud_image_datastore
  node_name    = var.target_node
  url          = var.cloud_image_url
  file_name    = var.cloud_image_file_name
}

resource "proxmox_virtual_environment_vm" "import" {
  for_each = var.vm_source == "import" ? var.vms : {}

  node_name = var.target_node
  name      = each.key
  on_boot   = true
  started   = true

  # Диск импортируется напрямую из скачанного образа — без блока clone.
  disk {
    datastore_id = "local-lvm"
    import_from  = proxmox_download_file.cloud_image[0].id
    interface    = "scsi0"
    size         = each.value.disk # можно задать больше размера образа — диск расширится
  }

  cpu {
    cores = each.value.cores
    type  = "host"
  }

  memory {
    dedicated = each.value.memory
  }

  network_device {
    bridge = "vmbr0"
  }

  # Обязателен для Ubuntu/Debian из cloud image: без serial device VM уходит
  # в kernel panic при ресайзе boot-диска (в clone-шаблоне он уже задан через
  # qm set --serial0). См. README провайдера bpg, issues #1639/#1770.
  serial_device {
    device = "socket"
  }

  # Cloud-init: тот же блок, что и при клонировании.
  initialization {
    user_account {
      username = var.default_user
      keys     = [var.ssh_public_key]
    }

    ip_config {
      ipv4 {
        address = each.value.ip
        gateway = var.gateway
      }
    }

    dns {
      servers = var.dns_servers
    }
  }
}
