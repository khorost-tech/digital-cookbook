# Вариант «из клона шаблона». Активен при vm_source = "clone".
# Требует заранее подготовленного VM-шаблона с cloud-init (см. README → «Шаблон для clone»).
resource "proxmox_virtual_environment_vm" "clone" {
  for_each = var.vm_source == "clone" ? var.vms : {}

  node_name = var.target_node
  name      = each.key
  on_boot   = true
  started   = true

  # Полная копия диска — VM независима от шаблона.
  clone {
    vm_id = var.vm_template_id
    full  = true
  }

  cpu {
    cores = each.value.cores
    type  = "host"
  }

  memory {
    dedicated = each.value.memory
  }

  disk {
    interface    = "scsi0"
    size         = each.value.disk
    datastore_id = "local-lvm"
  }

  network_device {
    bridge = "vmbr0"
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
