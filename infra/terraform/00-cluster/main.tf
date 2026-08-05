# Layer 00 — the cluster itself.
#
# Split into numbered root modules rather than one state on purpose: the Kafka
# and RabbitMQ providers in layer 20 need those brokers *running* in order to
# initialize. A single `terraform apply` cannot express that, so the ordering is
# enforced by the root Makefile instead.
#
# This layer creates the cluster and nothing else. Namespaces and quotas live in
# layer 10, because a `kubernetes` provider configured from an attribute of a
# resource created in the same apply is not reliably resolvable at plan time.

terraform {
  required_version = ">= 1.9"

  required_providers {
    k3d = {
      source  = "SneakyBugs/k3d"
      version = "~> 1.0"
    }
  }

  # Local state is deliberate — this cluster is disposable. Standing up remote
  # state for something recreated with `make infra-down && make infra-up` would
  # be ceremony without benefit.
  backend "local" {
    path = "terraform.tfstate"
  }
}

variable "cluster_name" {
  type    = string
  default = "pipsim"
}

variable "agent_count" {
  description = "Worker nodes. Two is enough to make scheduling and anti-affinity observable without cooking the laptop."
  type        = number
  default     = 2
}

variable "registry_port" {
  description = "Host port for the in-cluster registry Tilt pushes images to."
  type        = number
  default     = 5050
}

# The provider takes k3d's own config format rather than exposing individual
# arguments, so this is the same YAML you would hand to `k3d cluster create
# --config`. Awkward for validation — Terraform cannot check inside the string —
# but it does mean the file stays in step with k3d's documentation.
resource "k3d_cluster" "pipsim" {
  name = var.cluster_name

  k3d_config = yamlencode({
    apiVersion = "k3d.io/v1alpha5"
    kind       = "Simple"
    metadata   = { name = var.cluster_name }

    servers = 1
    agents  = var.agent_count

    # A local registry so Tilt can push images without a round trip to a
    # remote one — that round trip is most of the edit-loop latency otherwise.
    registries = {
      create = {
        name     = "${var.cluster_name}-registry"
        host     = "0.0.0.0"
        hostPort = tostring(var.registry_port)
      }
    }

    ports = [
      {
        port        = "8080:80"
        nodeFilters = ["loadbalancer"]
      },
    ]
  })
}

output "cluster_name" {
  value = k3d_cluster.pipsim.name
}

output "kube_context" {
  value = "k3d-${var.cluster_name}"
}

output "registry" {
  value = "localhost:${var.registry_port}"
}

# Consumed by layer 10 if you would rather wire the providers from state than
# from ~/.kube/config.
output "kubeconfig" {
  value     = k3d_cluster.pipsim.kubeconfig
  sensitive = true
}

output "host" {
  value = k3d_cluster.pipsim.host
}
