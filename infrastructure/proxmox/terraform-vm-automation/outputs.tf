# IP-адреса созданных VM — объединяем оба варианта (активен всегда один).
output "vm_ips" {
  description = "IP-адреса созданных VM"
  value = merge(
    {
      for name, vm in proxmox_virtual_environment_vm.clone :
      name => vm.initialization[0].ip_config[0].ipv4[0].address
    },
    {
      for name, vm in proxmox_virtual_environment_vm.import :
      name => vm.initialization[0].ip_config[0].ipv4[0].address
    },
  )
}

output "container_ips" {
  description = "IP-адреса созданных LXC-контейнеров"
  value = {
    for name, ct in proxmox_virtual_environment_container.lab :
    name => ct.initialization[0].ip_config[0].ipv4[0].address
  }
}
