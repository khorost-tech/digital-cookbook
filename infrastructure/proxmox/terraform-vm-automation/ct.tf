# LXC-контейнеры. Не зависят от vm_source — создаются по карте var.containers.
# Для LXC используются системные шаблоны (pveam), а не cloud image.
resource "proxmox_virtual_environment_container" "lab" {
  for_each = var.containers

  node_name = var.target_node

  initialization {
    hostname = each.key

    ip_config {
      ipv4 {
        address = each.value.ip
        gateway = var.gateway
      }
    }

    user_account {
      keys = [var.ssh_public_key]
    }

    dns {
      servers = var.dns_servers
    }
  }

  cpu {
    cores = each.value.cores
  }

  memory {
    dedicated = each.value.memory
  }

  disk {
    datastore_id = "local-lvm"
    size         = each.value.disk
  }

  network_interface {
    name   = "eth0"
    bridge = "vmbr0"
  }

  operating_system {
    template_file_id = each.value.template
    type             = "debian"
  }

  features {
    nesting = true # для Docker внутри LXC
  }

  started = true
}
