# Layer 10 — the platform: brokers, cache, database, telemetry.
#
# Everything here is a Helm release. Terraform owns it because it changes
# rarely; Tilt owns our own services because they change every few seconds.
# Mixing the two would put a 30-second `terraform apply` in the middle of the
# edit loop.

terraform {
  required_version = ">= 1.9"

  required_providers {
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.16"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.33"
    }
  }

  backend "local" {
    path = "terraform.tfstate"
  }
}

variable "kube_context" {
  type    = string
  default = "k3d-pipsim"
}

variable "namespace" {
  type    = string
  default = "pipsim-platform"
}

provider "helm" {
  kubernetes {
    config_path    = "~/.kube/config"
    config_context = var.kube_context
  }
}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = var.kube_context
}

# Namespaces live here rather than in layer 00 because a provider configured
# from an attribute of a resource created in the same apply is not reliably
# resolvable at plan time. By this layer the cluster already exists.
#
# Application and platform namespaces are separate so `make infra-down` can drop
# the platform without touching running services, and so quotas can differ.
resource "kubernetes_namespace" "pipsim" {
  metadata {
    name   = "pipsim"
    labels = { "app.kubernetes.io/part-of" = "pipsim" }
  }
}

resource "kubernetes_namespace" "platform" {
  metadata {
    name   = var.namespace
    labels = { "app.kubernetes.io/part-of" = "pipsim" }
  }
}

# A quota, because the point of running this locally is to feel the constraints.
# Without one, a runaway service just eats the laptop.
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

# Redpanda rather than Kafka: same wire protocol, single binary, no ZooKeeper
# and no KRaft ceremony, and roughly an order of magnitude lighter on a laptop.
# Everything speaks to it with ordinary Kafka clients.
resource "helm_release" "redpanda" {
  name       = "redpanda"
  repository = "https://charts.redpanda.com"
  chart      = "redpanda"
  version    = "5.9.5"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  set {
    name  = "statefulset.replicas"
    value = 1
  }
  set {
    name  = "resources.cpu.cores"
    value = 1
  }
  # TLS and auth off locally on purpose: this cluster is disposable and the
  # certificates would only get in the way of `rpk` and Kafka console.
  set {
    name  = "tls.enabled"
    value = false
  }
  set {
    name  = "auth.sasl.enabled"
    value = false
  }
  set {
    name  = "console.enabled"
    value = true
  }
}

resource "helm_release" "rabbitmq" {
  name       = "rabbitmq"
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "rabbitmq"
  version    = "15.0.5"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  set {
    name  = "auth.username"
    value = "pipsim"
  }
  set {
    name  = "auth.password"
    value = "pipsim"
  }
  set {
    name  = "metrics.enabled"
    value = true
  }
}

resource "helm_release" "redis" {
  name       = "redis"
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "redis"
  version    = "20.2.1"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  set {
    name  = "architecture"
    value = "standalone"
  }
  set {
    name  = "auth.enabled"
    value = false
  }
}

resource "helm_release" "postgres" {
  name       = "postgres"
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "postgresql"
  version    = "16.2.1"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  set {
    name  = "auth.postgresPassword"
    value = "pipsim"
  }
  set {
    name  = "auth.database"
    value = "pipsim"
  }
}

# Traces from six languages are only readable in one place if every service
# exports to the same collector. With this many runtimes, distributed tracing
# stops being a nice-to-have and becomes the only way to debug anything.
resource "helm_release" "otel_collector" {
  name       = "otel-collector"
  repository = "https://open-telemetry.github.io/opentelemetry-helm-charts"
  chart      = "opentelemetry-collector"
  version    = "0.108.0"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  set {
    name  = "mode"
    value = "deployment"
  }
  set {
    name  = "image.repository"
    value = "otel/opentelemetry-collector-contrib"
  }
}

resource "helm_release" "jaeger" {
  name       = "jaeger"
  repository = "https://jaegertracing.github.io/helm-charts"
  chart      = "jaeger"
  version    = "3.3.1"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  set {
    name  = "allInOne.enabled"
    value = true
  }
  set {
    name  = "storage.type"
    value = "memory"
  }
}

output "kafka_bootstrap" {
  value = "redpanda.${var.namespace}.svc.cluster.local:9093"
}

output "rabbitmq_host" {
  value = "rabbitmq.${var.namespace}.svc.cluster.local"
}

output "postgres_host" {
  value = "postgres-postgresql.${var.namespace}.svc.cluster.local"
}
