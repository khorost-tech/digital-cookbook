variable "proxmox_url" {
  description = "URL Proxmox API, напр. https://pve.local:8006/"
  type        = string
}

variable "proxmox_token_id" {
  description = "ID API-токена, напр. terraform@pam!tf-token"
  type        = string
}

variable "proxmox_token_secret" {
  description = "Секрет API-токена"
  type        = string
  sensitive   = true
}

variable "target_node" {
  description = "Имя ноды Proxmox"
  type        = string
}

variable "cloud_image_url" {
  description = "URL cloud image (qcow2)"
  type        = string
  default     = "https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img"
}

variable "default_user" {
  description = "Пользователь, создаваемый cloud-init"
  type        = string
  default     = "ops"
}

variable "ssh_public_key" {
  description = "Публичный SSH-ключ для доступа Ansible к хостам"
  type        = string
}

variable "gateway" {
  description = "Шлюз по умолчанию для VM"
  type        = string
}

variable "dns_servers" {
  description = "DNS-серверы для VM"
  type        = list(string)
  default     = ["1.1.1.1", "8.8.8.8"]
}

# Карта docker-хостов: имя → параметры. Добавить хост = одна запись.
variable "docker_hosts" {
  description = "Docker-хосты, создаваемые Terraform"
  type = map(object({
    cores  = number
    memory = number
    disk   = number
    ip     = string # CIDR, напр. 10.10.10.111/24
  }))
}
