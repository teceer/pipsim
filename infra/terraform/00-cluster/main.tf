# Layer 00 — the cluster itself.
#
# Split into numbered root modules rather than one state on purpose: the Kafka
# and RabbitMQ providers in layer 20 need those brokers *running* in order to
# initialize. A single `terraform apply` cannot express that, so the ordering is
# enforced by the root Makefile instead.

terraform {
  required_version = ">= 1.9"

  required_providers {
    k3d = {
      source  = "SneakyBugs/k3d"
      version = "~> 0.0.6"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.33"
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

resource "k3d_cluster" "pipsim" {
  name    = var.cluster_name
  servers = 1
  agents  = var.agent_count

  # The registry lets Tilt push images without a round trip to a remote one.
  registry_create {
    name      = "pipsim-registry"
    host_port = 5050
  }

  port {
    host_port      = 8080
    container_port = 80
    node_filters   = ["loadbalancer"]
  }
}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "k3d-${var.cluster_name}"
}

# Application namespace and platform namespace are separate so that
# `make infra-down` can drop the platform without touching running services,
# and so quotas can differ.
resource "kubernetes_namespace" "pipsim" {
  depends_on = [k3d_cluster.pipsim]
  metadata {
    name   = "pipsim"
    labels = { "app.kubernetes.io/part-of" = "pipsim" }
  }
}

resource "kubernetes_namespace" "platform" {
  depends_on = [k3d_cluster.pipsim]
  metadata {
    name   = "pipsim-platform"
    labels = { "app.kubernetes.io/part-of" = "pipsim" }
  }
}

# A quota, because the whole point of running this locally is to feel the
# constraints. Without one, a runaway service just eats the laptop.
resource "kubernetes_resource_quota" "pipsim" {
  metadata {
    name      = "pipsim-quota"
    namespace = kubernetes_namespace.pipsim.metadata[0].name
  }
  spec {
    hard = {
      "requests.cpu"    = "4"
      "requests.memory" = "8Gi"
      "pods"            = "40"
    }
  }
}

output "cluster_name" {
  value = k3d_cluster.pipsim.name
}

output "kube_context" {
  value = "k3d-${var.cluster_name}"
}

output "registry" {
  value = "localhost:5050"
}
