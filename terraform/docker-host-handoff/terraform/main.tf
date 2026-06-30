# Terraform сам скачивает cloud image на ноду (механика — в статье proxmox-terraform).
resource "proxmox_download_file" "cloud_image" {
  content_type = "import"
  datastore_id = "local"
  node_name    = var.target_node

  url       = var.cloud_image_url
  file_name = "docker-host-base.qcow2"
}

# Docker-хост: VM, которой дальше владеет Ansible на уровне ОС/контейнеров.
resource "proxmox_virtual_environment_vm" "docker_host" {
  for_each = var.docker_hosts

  node_name = var.target_node
  name      = each.key
  on_boot   = true
  started   = true

  disk {
    datastore_id = "local-lvm"
    import_from  = proxmox_download_file.cloud_image.id
    interface    = "scsi0"
    size         = each.value.disk
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

  # Без serial device Ubuntu/Debian из cloud image уходят в kernel panic при ресайзе диска.
  serial_device {
    device = "socket"
  }

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
