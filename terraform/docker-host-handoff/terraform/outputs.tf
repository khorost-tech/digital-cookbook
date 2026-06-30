# Карта host → IP (без CIDR-маски). Это контракт для Ansible-слоя.
output "docker_hosts" {
  description = "Созданные docker-хосты: имя → IP"
  value = {
    for name, vm in proxmox_virtual_environment_vm.docker_host :
    name => split("/", vm.initialization[0].ip_config[0].ipv4[0].address)[0]
  }
}
