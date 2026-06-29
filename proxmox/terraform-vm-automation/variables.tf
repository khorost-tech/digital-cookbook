# --- Доступ к Proxmox API ---

variable "proxmox_url" {
  description = "Базовый URL Proxmox, например https://pve.lab.local:8006"
  type        = string
}

variable "proxmox_token_id" {
  description = "ID API-токена, например terraform@pam!tf-token"
  type        = string
}

variable "proxmox_token_secret" {
  description = "Секрет API-токена"
  type        = string
  sensitive   = true
}

variable "target_node" {
  description = "Имя ноды Proxmox, на которой создаются ресурсы"
  type        = string
}

# --- Выбор способа создания VM ---

variable "vm_source" {
  description = "Способ создания VM: clone (из заранее подготовленного шаблона) или import (Terraform сам качает cloud image)"
  type        = string
  default     = "import"

  validation {
    condition     = contains(["clone", "import"], var.vm_source)
    error_message = "vm_source должен быть \"clone\" или \"import\"."
  }
}

# --- Параметры варианта clone ---

variable "vm_template_id" {
  description = "ID VM-шаблона для клонирования (нужен только при vm_source = clone)"
  type        = number
  default     = 9000
}

# --- Параметры варианта import ---

variable "cloud_image_url" {
  description = "URL cloud image (нужен только при vm_source = import)"
  type        = string
  default     = "https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img"
}

variable "cloud_image_file_name" {
  description = "Имя файла образа на storage. Расширение .qcow2 — чтобы Proxmox распознал формат Ubuntu .img"
  type        = string
  default     = "resolute-server-cloudimg-amd64.qcow2"
}

variable "cloud_image_datastore" {
  description = "Storage для скачивания образа. Требует включённого content type import"
  type        = string
  default     = "local"
}

# --- Cloud-init / сеть (общее для обоих вариантов) ---

variable "default_user" {
  description = "Пользователь, создаваемый cloud-init в VM"
  type        = string
  default     = "ops"
}

variable "ssh_public_key" {
  description = "Публичный SSH-ключ для cloud-init (VM и LXC)"
  type        = string
}

variable "gateway" {
  description = "Шлюз по умолчанию"
  type        = string
  default     = "10.10.10.1"
}

variable "dns_servers" {
  description = "DNS-серверы для cloud-init"
  type        = list(string)
  default     = ["1.1.1.1", "8.8.8.8"]
}

# --- Описание стенда ---

variable "vms" {
  description = "Карта VM: имя -> параметры"
  type = map(object({
    cores  = number
    memory = number
    disk   = number
    ip     = string # CIDR, например 10.10.10.101/24
  }))
  default = {}
}

variable "containers" {
  description = "Карта LXC-контейнеров: имя -> параметры"
  type = map(object({
    cores    = number
    memory   = number
    disk     = number
    ip       = string # CIDR
    template = string # local:vztmpl/...
  }))
  default = {}
}
