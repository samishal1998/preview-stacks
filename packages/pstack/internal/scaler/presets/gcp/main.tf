# pstack-distro: ubuntu
#
# pstack worker scaler preset: Google Compute Engine. Credential: GOOGLE_CREDENTIALS (service-account JSON).
# GCE's Debian images do not run cloud-init, so workers boot Ubuntu.

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.10"
    }
  }
}

variable "workers" {
  type = map(object({
    user_data    = string
    machine_type = string
    zone         = string
  }))
}

variable "host_id" {
  type = string
}

variable "project" {
  type = string
}

# The manager's subnetwork: projects/<project>/regions/<region>/subnetworks/<name>, or its full self link.
variable "subnetwork" {
  type = string
}

provider "google" {
  project = var.project
}

data "google_compute_subnetwork" "manager" {
  self_link = "https://www.googleapis.com/compute/v1/${trimprefix(var.subnetwork, "https://www.googleapis.com/compute/v1/")}"
}

resource "google_compute_firewall" "workers" {
  name          = "pstack-scaler-${var.host_id}"
  network       = data.google_compute_subnetwork.manager.network
  source_ranges = [data.google_compute_subnetwork.manager.ip_cidr_range]
  target_tags   = ["pstack-scaler-${var.host_id}"]

  allow {
    protocol = "tcp"
    ports    = ["7946"]
  }
  allow {
    protocol = "udp"
    ports    = ["7946", "4789"]
  }
}

resource "google_compute_instance" "worker" {
  for_each     = var.workers
  name         = each.key
  machine_type = each.value.machine_type
  zone         = each.value.zone
  labels       = { "pstack-scaler" = var.host_id }
  tags         = ["pstack-scaler-${var.host_id}"]
  metadata     = { "user-data" = each.value.user_data }

  boot_disk {
    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
    }
  }

  network_interface {
    subnetwork = data.google_compute_subnetwork.manager.self_link
    access_config {}
  }

  lifecycle {
    ignore_changes = [metadata["user-data"], boot_disk[0].initialize_params[0].image]
  }
}
