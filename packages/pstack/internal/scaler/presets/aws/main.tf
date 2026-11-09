# pstack-distro: ubuntu
#
# pstack worker scaler preset: AWS EC2. Credential: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY.
# subnet_id is in the manager's VPC; the manager's security group admits the VPC's CIDR blocks.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.72"
    }
  }
}

variable "workers" {
  type = map(object({
    user_data     = string
    instance_type = string
  }))
}

variable "host_id" {
  type = string
}

variable "region" {
  type = string
}

variable "subnet_id" {
  type = string
}

# An EC2 key pair name; empty launches without one.
variable "key_name" {
  type    = string
  default = ""
}

provider "aws" {
  region = var.region
}

data "aws_subnet" "manager" {
  id = var.subnet_id
}

data "aws_vpc" "manager" {
  id = data.aws_subnet.manager.vpc_id
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
}

resource "aws_security_group" "workers" {
  name   = "pstack-scaler-${var.host_id}"
  vpc_id = data.aws_vpc.manager.id
  tags   = { "pstack-scaler" = var.host_id }

  ingress {
    protocol    = "tcp"
    from_port   = 7946
    to_port     = 7946
    cidr_blocks = data.aws_vpc.manager.cidr_block_associations[*].cidr_block
    self        = true
  }
  ingress {
    protocol    = "udp"
    from_port   = 7946
    to_port     = 7946
    cidr_blocks = data.aws_vpc.manager.cidr_block_associations[*].cidr_block
    self        = true
  }
  ingress {
    protocol    = "udp"
    from_port   = 4789
    to_port     = 4789
    cidr_blocks = data.aws_vpc.manager.cidr_block_associations[*].cidr_block
    self        = true
  }

  # Terraform removes AWS's default egress rule; workers need it to reach Docker and image registries.
  egress {
    protocol         = "-1"
    from_port        = 0
    to_port          = 0
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
}

resource "aws_instance" "worker" {
  for_each                    = var.workers
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = each.value.instance_type
  subnet_id                   = var.subnet_id
  key_name                    = var.key_name == "" ? null : var.key_name
  vpc_security_group_ids      = [aws_security_group.workers.id]
  associate_public_ip_address = true
  user_data                   = each.value.user_data
  tags                        = { "pstack-scaler" = var.host_id, Name = each.key }

  lifecycle {
    ignore_changes = [user_data, ami]
  }
}
