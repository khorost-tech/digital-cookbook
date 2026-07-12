# Главный приём handoff: Terraform рендерит Ansible inventory из своих же данных.
# После apply Ansible получает готовый, всегда актуальный inventory.
resource "local_file" "ansible_inventory" {
  filename = "${path.module}/../ansible/inventory/hosts.ini"

  content = templatefile("${path.module}/templates/hosts.ini.tmpl", {
    hosts = {
      for name, vm in proxmox_virtual_environment_vm.docker_host :
      name => split("/", vm.initialization[0].ip_config[0].ipv4[0].address)[0]
    }
    user = var.default_user
  })

  file_permission = "0640"
}
