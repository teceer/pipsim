# Layer 10 — the platform: brokers, cache, database, telemetry.
#
# Terraform owns this because it changes rarely; Tilt owns our own services
# because they change every few seconds. Mixing the two would put a 30-second
# `terraform apply` in the middle of the edit loop.
#
# Helm is used for Redpanda only, where a chart genuinely earns its keep —
# StatefulSet, PVCs, service discovery, console. Everything else is a plain
# Deployment with the *same upstream image compose.dev.yaml uses*, which means
# the local docker-compose loop and the cluster run identical software.
#
# That is a deliberate reversal. The Bitnami charts this layer used to reference
# are unusable: Bitnami moved its free images to `bitnamilegacy/` in 2025, so
# charts redis 20.2.1, postgresql 16.2.1 and rabbitmq 15.0.5 all point at image
# tags that no longer exist on Docker Hub. Every pod would sit in
# ImagePullBackOff. For a disposable local cluster a twenty-line Deployment with
# the official image beats a several-thousand-line chart whose images depend on
# a vendor's licensing decisions.

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

# ---------------------------------------------------------------------------
# A single-replica Deployment plus Service. Everything here is stateless-by-
# choice: this cluster is disposable, and losing its data on teardown is the
# intended behaviour, not a limitation to work around.
# ---------------------------------------------------------------------------

locals {
  simple_services = {
    postgres = {
      image = "postgres:17-alpine"
      port  = 5432
      env = {
        POSTGRES_PASSWORD = "pipsim"
        POSTGRES_USER     = "pipsim"
        POSTGRES_DB       = "pipsim"
      }
      extra_ports = {}
    }
    redis = {
      image       = "redis:7-alpine"
      port        = 6379
      env         = {}
      extra_ports = {}
    }
    rabbitmq = {
      image = "rabbitmq:4-management-alpine"
      port  = 5672
      env = {
        RABBITMQ_DEFAULT_USER = "pipsim"
        RABBITMQ_DEFAULT_PASS = "pipsim"
      }
      # The management API is what the Terraform rabbitmq provider in layer 20
      # talks to — it speaks HTTP, not AMQP.
      extra_ports = { management = 15672 }
    }
  }
}

resource "kubernetes_deployment" "simple" {
  for_each = local.simple_services

  metadata {
    name      = each.key
    namespace = kubernetes_namespace.platform.metadata[0].name
    labels    = { app = each.key }
  }

  spec {
    replicas = 1
    selector {
      match_labels = { app = each.key }
    }

    template {
      metadata {
        labels = { app = each.key }
      }

      spec {
        container {
          name  = each.key
          image = each.value.image

          port {
            container_port = each.value.port
          }

          dynamic "port" {
            for_each = each.value.extra_ports
            content {
              name           = port.key
              container_port = port.value
            }
          }

          dynamic "env" {
            for_each = each.value.env
            content {
              name  = env.key
              value = env.value
            }
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "256Mi"
            }
          }
        }
      }
    }
  }
}

resource "kubernetes_service" "simple" {
  for_each = local.simple_services

  metadata {
    name      = each.key
    namespace = kubernetes_namespace.platform.metadata[0].name
  }

  spec {
    selector = { app = each.key }

    port {
      name        = "main"
      port        = each.value.port
      target_port = each.value.port
    }

    dynamic "port" {
      for_each = each.value.extra_ports
      content {
        name        = port.key
        port        = port.value
        target_port = port.value
      }
    }
  }
}

# ---------------------------------------------------------------------------
# Redpanda — Kafka wire protocol, single binary, no ZooKeeper or KRaft ceremony,
# and roughly an order of magnitude lighter than Kafka on a laptop.
# ---------------------------------------------------------------------------

resource "helm_release" "redpanda" {
  name       = "redpanda"
  repository = "https://charts.redpanda.com"
  chart      = "redpanda"
  version    = "5.9.5"
  namespace  = kubernetes_namespace.platform.metadata[0].name

  # Charts take a while to settle on a small cluster; failing fast here just
  # means re-running the whole layer.
  timeout = 900
  wait    = true

  set {
    name  = "statefulset.replicas"
    value = 1
  }
  # Must be sent as a string: the chart feeds this straight into a Kubernetes
  # Quantity, which rejects a bare int64.
  set {
    name  = "resources.cpu.cores"
    value = "1"
    type  = "string"
  }
  set {
    name  = "resources.memory.container.max"
    value = "1.5Gi"
  }
  set {
    name  = "storage.persistentVolume.size"
    value = "5Gi"
  }
  # TLS and auth off locally on purpose: this cluster is disposable and the
  # certificates would only get in the way of rpk and the console.
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
  # Advertise a host-resolvable address on the external listener. The default is
  # the node's in-cluster IP, which a client on the laptop cannot route to, so a
  # connection would bootstrap and then immediately fail.
  set {
    name  = "external.addresses[0]"
    value = "localhost"
    type  = "string"
  }
  # Pin the broker to the server node, which is the one whose NodePort layer 00
  # maps to the host.
  #
  # The chart's external Service uses externalTrafficPolicy: Local and does not
  # expose an override, so a NodePort is only answered by the node actually
  # running the pod. With the broker on an agent, a client on the host connects
  # and then gets EOF — the connection reaches k3d's proxy but no backend.
  # Pinning makes placement deterministic, which is fine for a single broker.
  set {
    name  = "nodeSelector.node-role\\.kubernetes\\.io/control-plane"
    value = "true"
    type  = "string"
  }
  # A single node cannot satisfy the chart's default anti-affinity.
  set {
    name  = "statefulset.budget.maxUnavailable"
    value = 1
  }
}

# ---------------------------------------------------------------------------
# Telemetry.
#
# With six languages in the repo, distributed tracing is not an optimisation —
# it is the only way to follow a request that crosses Rust, Go, Elixir and
# TypeScript in one chain. Every service exports to this collector; the
# collector is the only thing that knows where traces actually go.
# ---------------------------------------------------------------------------

resource "kubernetes_deployment" "jaeger" {
  metadata {
    name      = "jaeger"
    namespace = kubernetes_namespace.platform.metadata[0].name
    labels    = { app = "jaeger" }
  }

  spec {
    replicas = 1
    selector {
      match_labels = { app = "jaeger" }
    }
    template {
      metadata {
        labels = { app = "jaeger" }
      }
      spec {
        container {
          name  = "jaeger"
          image = "jaegertracing/all-in-one:1.76.0"

          # In-memory storage: traces vanish on restart, which is correct for a
          # cluster that is torn down daily.
          env {
            name  = "SPAN_STORAGE_TYPE"
            value = "memory"
          }
          env {
            name  = "COLLECTOR_OTLP_ENABLED"
            value = "true"
          }

          port {
            name           = "ui"
            container_port = 16686
          }
          port {
            name           = "otlp-grpc"
            container_port = 4317
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "256Mi"
            }
          }
        }
      }
    }
  }
}

resource "kubernetes_service" "jaeger" {
  metadata {
    name      = "jaeger"
    namespace = kubernetes_namespace.platform.metadata[0].name
  }
  spec {
    selector = { app = "jaeger" }
    port {
      name        = "ui"
      port        = 16686
      target_port = 16686
    }
    port {
      name        = "otlp-grpc"
      port        = 4317
      target_port = 4317
    }
  }
}

resource "kubernetes_config_map" "otel_collector" {
  metadata {
    name      = "otel-collector-config"
    namespace = kubernetes_namespace.platform.metadata[0].name
  }

  data = {
    "config.yaml" = yamlencode({
      receivers = {
        otlp = {
          protocols = {
            grpc = { endpoint = "0.0.0.0:4317" }
            http = { endpoint = "0.0.0.0:4318" }
          }
        }
      }
      processors = {
        batch = { timeout = "1s" }
        # Every service tags itself identically, so traces are filterable by
        # service regardless of which language emitted them.
        resource = {
          attributes = [
            { key = "deployment.environment", value = "local", action = "upsert" },
          ]
        }
      }
      exporters = {
        "otlp/jaeger" = {
          endpoint = "jaeger.${var.namespace}.svc.cluster.local:4317"
          tls      = { insecure = true }
        }
        debug = { verbosity = "normal" }
      }
      service = {
        pipelines = {
          traces = {
            receivers  = ["otlp"]
            processors = ["resource", "batch"]
            exporters  = ["otlp/jaeger"]
          }
          metrics = {
            receivers  = ["otlp"]
            processors = ["resource", "batch"]
            exporters  = ["debug"]
          }
        }
      }
    })
  }
}

resource "kubernetes_deployment" "otel_collector" {
  metadata {
    name      = "otel-collector"
    namespace = kubernetes_namespace.platform.metadata[0].name
    labels    = { app = "otel-collector" }
  }

  spec {
    replicas = 1
    selector {
      match_labels = { app = "otel-collector" }
    }
    template {
      metadata {
        labels = { app = "otel-collector" }
        annotations = {
          # Roll the pod when the config changes; a ConfigMap edit alone does
          # not restart anything.
          "pipsim.dev/config-hash" = sha256(kubernetes_config_map.otel_collector.data["config.yaml"])
        }
      }
      spec {
        container {
          name  = "otel-collector"
          image = "otel/opentelemetry-collector-contrib:0.158.0"
          args  = ["--config=/etc/otel/config.yaml"]

          volume_mount {
            name       = "config"
            mount_path = "/etc/otel"
          }

          port {
            name           = "otlp-grpc"
            container_port = 4317
          }
          port {
            name           = "otlp-http"
            container_port = 4318
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "256Mi"
            }
          }
        }

        volume {
          name = "config"
          config_map {
            name = kubernetes_config_map.otel_collector.metadata[0].name
          }
        }
      }
    }
  }
}

resource "kubernetes_service" "otel_collector" {
  metadata {
    name      = "otel-collector"
    namespace = kubernetes_namespace.platform.metadata[0].name
  }
  spec {
    selector = { app = "otel-collector" }
    port {
      name        = "otlp-grpc"
      port        = 4317
      target_port = 4317
    }
    port {
      name        = "otlp-http"
      port        = 4318
      target_port = 4318
    }
  }
}

# ---------------------------------------------------------------------------
# Outputs consumed by layer 20 and by the Makefile's port-forwards.
# ---------------------------------------------------------------------------

output "kafka_bootstrap" {
  value = "redpanda.${var.namespace}.svc.cluster.local:9093"
}

output "rabbitmq_host" {
  value = kubernetes_service.simple["rabbitmq"].metadata[0].name
}

output "postgres_host" {
  value = kubernetes_service.simple["postgres"].metadata[0].name
}

output "otel_endpoint" {
  value = "http://otel-collector.${var.namespace}.svc.cluster.local:4317"
}
