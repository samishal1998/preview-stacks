# pstack-distro: debian
#
# pstack worker scaler preset: Hetzner Cloud. Credential: HCLOUD_TOKEN.
# Put the manager under the firewall too: label it pstack-scaler=<host_id>. Only once the manager
# already has a firewall admitting 80/443: any firewall that applies denies every other inbound port.

terraform {
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.70"
    }
  }
}

variable "workers" {
  type = map(object({
    user_data   = string
    server_type = string
    location    = string
  }))
}

variable "host_id" {
  type = string
}

variable "manager_address" {
  type = string
}

variable "ssh_keys" {
  type    = list(string)
  default = []
}

# A Hetzner Cloud Network id; empty keeps workers on public addresses only.
variable "network_id" {
  type    = string
  default = ""
}

resource "hcloud_server" "worker" {
  for_each    = var.workers
  name        = each.key
  image       = "debian-12"
  server_type = each.value.server_type
  location    = each.value.location
  user_data   = each.value.user_data
  ssh_keys    = var.ssh_keys
  labels      = { "pstack-scaler" = var.host_id }

  dynamic "network" {
    for_each = var.network_id == "" ? [] : [var.network_id]
    content {
      network_id = tonumber(network.value)
    }
  }

  lifecycle {
    ignore_changes = [user_data, image]
  }
}

locals {
  swarm_sources = concat(
    ["${var.manager_address}/32"],
    [for s in hcloud_server.worker : "${s.ipv4_address}/32"],
  )
  anywhere = ["0.0.0.0/0", "::/0"]
}

# Applied by label only: firewall_ids on the server as well would form a cycle.
resource "hcloud_firewall" "workers" {
  name = "pstack-scaler-${var.host_id}"

  apply_to {
    label_selector = "pstack-scaler=${var.host_id}"
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "2377"
    source_ips = local.swarm_sources
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "7946"
    source_ips = local.swarm_sources
  }
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "7946"
    source_ips = local.swarm_sources
  }
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "4789"
    source_ips = local.swarm_sources
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = local.anywhere
  }

  rule {
    direction       = "out"
    protocol        = "tcp"
    port            = "any"
    destination_ips = local.anywhere
  }
  rule {
    direction       = "out"
    protocol        = "udp"
    port            = "any"
    destination_ips = local.anywhere
  }
  rule {
    direction       = "out"
    protocol        = "icmp"
    destination_ips = local.anywhere
  }
}
