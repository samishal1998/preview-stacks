# pstack-distro: ubuntu
#
# pstack worker scaler preset: Azure. Credential: ARM_CLIENT_ID, ARM_CLIENT_SECRET, ARM_TENANT_ID,
# ARM_SUBSCRIPTION_ID (a service principal). No firewall: AllowVnetInBound admits the VNet.

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.13"
    }
  }
}

variable "workers" {
  type = map(object({
    user_data = string
    vm_size   = string
  }))
}

variable "host_id" {
  type = string
}

variable "resource_group" {
  type = string
}

variable "location" {
  type = string
}

# A subnet of the manager's VNet (its full resource id).
variable "subnet_id" {
  type = string
}

variable "admin_ssh_public_key" {
  type = string
}

provider "azurerm" {
  features {}
}

locals {
  tags = { "pstack-scaler" = var.host_id }
}

resource "azurerm_public_ip" "worker" {
  for_each            = var.workers
  name                = each.key
  resource_group_name = var.resource_group
  location            = var.location
  allocation_method   = "Static"
  sku                 = "Standard"
  tags                = local.tags
}

resource "azurerm_network_interface" "worker" {
  for_each            = var.workers
  name                = each.key
  resource_group_name = var.resource_group
  location            = var.location
  tags                = local.tags

  ip_configuration {
    name                          = "primary"
    subnet_id                     = var.subnet_id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.worker[each.key].id
  }
}

resource "azurerm_linux_virtual_machine" "worker" {
  for_each              = var.workers
  name                  = each.key
  computer_name         = each.key
  resource_group_name   = var.resource_group
  location              = var.location
  size                  = each.value.vm_size
  admin_username        = "pstack"
  network_interface_ids = [azurerm_network_interface.worker[each.key].id]
  custom_data           = base64encode(each.value.user_data)
  tags                  = local.tags

  admin_ssh_key {
    username   = "pstack"
    public_key = var.admin_ssh_public_key
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "ubuntu-24_04-lts"
    sku       = "server"
    version   = "latest"
  }

  lifecycle {
    ignore_changes = [custom_data, source_image_reference]
  }
}
