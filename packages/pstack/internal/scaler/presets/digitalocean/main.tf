# pstack-distro: ubuntu
#
# pstack worker scaler preset: DigitalOcean. Credential: DIGITALOCEAN_TOKEN.
# Put the manager under the firewall too: tag it pstack-scaler-<host_id>. Only once the manager
# already has a firewall admitting 80/443: any firewall that applies denies every other inbound port.

terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.107"
    }
  }
}

variable "workers" {
  type = map(object({
    user_data = string
    size      = string
    region    = string
  }))
}

variable "host_id" {
  type = string
}

variable "manager_address" {
  type = string
}

# SSH key ids or fingerprints.
variable "ssh_keys" {
  type    = list(string)
  default = []
}

# A VPC uuid; empty puts workers in the region's default VPC, on public addresses.
variable "vpc_uuid" {
  type    = string
  default = ""
}

# Declared so it exists before the firewall and droplets name it.
resource "digitalocean_tag" "host" {
  name = "pstack-scaler-${var.host_id}"
}

resource "digitalocean_tag" "worker" {
  for_each = var.workers
  name     = "pstack-scaler-${var.host_id}:${each.key}"
}

resource "digitalocean_droplet" "worker" {
  for_each  = var.workers
  name      = each.key
  image     = "ubuntu-24-04-x64"
  size      = each.value.size
  region    = each.value.region
  user_data = each.value.user_data
  ssh_keys  = var.ssh_keys
  vpc_uuid  = var.vpc_uuid == "" ? null : var.vpc_uuid
  tags      = [digitalocean_tag.host.id, digitalocean_tag.worker[each.key].id]

  lifecycle {
    ignore_changes = [user_data, image]
  }
}

locals {
  anywhere = ["0.0.0.0/0", "::/0"]
}

resource "digitalocean_firewall" "workers" {
  name = "pstack-scaler-${var.host_id}"
  tags = [digitalocean_tag.host.id]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "2377"
    source_addresses = [var.manager_address]
    source_tags      = [digitalocean_tag.host.id]
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "7946"
    source_addresses = [var.manager_address]
    source_tags      = [digitalocean_tag.host.id]
  }
  inbound_rule {
    protocol         = "udp"
    port_range       = "7946"
    source_addresses = [var.manager_address]
    source_tags      = [digitalocean_tag.host.id]
  }
  inbound_rule {
    protocol         = "udp"
    port_range       = "4789"
    source_addresses = [var.manager_address]
    source_tags      = [digitalocean_tag.host.id]
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = local.anywhere
  }

  # A DigitalOcean firewall with no outbound rules blocks all outbound.
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = local.anywhere
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = local.anywhere
  }
  outbound_rule {
    protocol              = "icmp"
    destination_addresses = local.anywhere
  }
}
