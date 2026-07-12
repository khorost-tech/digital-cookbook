# Переиспользуемая security group на уровне кластера:
# периметр docker-хоста (SSH + опубликованный сервис). Источник истины для сети — Terraform.
resource "proxmox_virtual_environment_cluster_firewall_security_group" "docker_host" {
  name    = "docker-host"
  comment = "Managed by Terraform — периметр docker-хоста"

  rule {
    type    = "in"
    action  = "ACCEPT"
    comment = "SSH (Ansible)"
    dport   = "22"
    proto   = "tcp"
    log     = "info"
  }

  rule {
    type    = "in"
    action  = "ACCEPT"
    comment = "HTTP (опубликованный сервис)"
    dport   = "80"
    proto   = "tcp"
    log     = "info"
  }

  rule {
    type    = "in"
    action  = "ACCEPT"
    comment = "HTTPS (опубликованный сервис)"
    dport   = "443"
    proto   = "tcp"
    log     = "info"
  }
}

# Привязка security group к каждому docker-хосту.
resource "proxmox_virtual_environment_firewall_rules" "docker_host" {
  for_each = proxmox_virtual_environment_vm.docker_host

  node_name = each.value.node_name
  vm_id     = each.value.vm_id

  rule {
    security_group = proxmox_virtual_environment_cluster_firewall_security_group.docker_host.name
    comment        = "Периметр docker-хоста"
  }
}

# Включаем firewall на интерфейсе VM.
resource "proxmox_virtual_environment_firewall_options" "docker_host" {
  for_each = proxmox_virtual_environment_vm.docker_host

  node_name = each.value.node_name
  vm_id     = each.value.vm_id

  enabled       = true
  input_policy  = "DROP"
  output_policy = "ACCEPT"
}
