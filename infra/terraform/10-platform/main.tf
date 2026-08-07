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

# The contracts, so the console can decode the fact log.
#
# Without them every message renders as a hex dump and a deserialization error:
# the console guesses JSON, Avro, msgpack and a dozen others, and protobuf is
# the one encoding it cannot guess. Same fix as compose, which mounts proto/
# directly — a cluster needs the files carried in.
#
# ConfigMap keys may not contain `/`, so they are flattened here and the
# directory structure is rebuilt by `items[].path` at mount time. That is not
# cosmetic: events.proto imports pips/sim/v1/sim.proto, and an import resolves
# against the mount root, so a flat directory would fail to load every file
# that imports another.
locals {
  proto_dir   = "${path.module}/../../../proto"
  proto_files = fileset("${path.module}/../../../proto", "**/*.proto")

  # Read from the file compose already uses, rather than restating the topic
  # mappings in HCL. One list of topics, one place to add the next one.
  console_protobuf = yamldecode(
    file("${path.module}/../../redpanda/console.yaml")
  ).kafka.protobuf
}

resource "kubernetes_config_map" "console_protos" {
  metadata {
    name      = "console-protos"
    namespace = kubernetes_namespace.platform.metadata[0].name
  }

  data = {
    for f in local.proto_files : replace(f, "/", "__") => file("${local.proto_dir}/${f}")
  }
}

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

  # Protobuf decoding for the console, as YAML rather than `set` blocks: the
  # mappings and volumes are lists of objects, and expressing those through
  # dotted-path indices is unreadable and easy to get subtly wrong.
  #
  # `config` merges into what the chart generates rather than replacing it, so
  # the broker addresses the subchart wires up on its own are left alone — only
  # the protobuf section is added.
  values = [yamlencode({
    console = {
      extraVolumes = [
        {
          name = "protos-src"
          configMap = {
            name = kubernetes_config_map.console_protos.metadata[0].name
            # key is the flattened ConfigMap entry, path rebuilds the directory
            # the imports expect.
            items = [
              for f in local.proto_files : {
                key  = replace(f, "/", "__")
                path = f
              }
            ]
          }
        },
        { name = "protos", emptyDir = {} },
      ]

      # A ConfigMap volume is not a plain directory: Kubernetes materialises it
      # as `..2026_08_07_13_56_39.../pips/...` with a `..data` symlink pointing
      # at the current one, so it can swap the contents atomically. The console
      # walks the tree recursively and takes each file's path *relative to the
      # mount* as its proto import path — which came out as
      # `..2026_08_07_.../pips/world/v1/world.proto`, and then
      # `import "pips/sim/v1/sim.proto"` resolved against nothing:
      #
      #   failed to parse proto file to descriptor:
      #   pips/sim/v1/sim.proto: file does not exist
      #
      # `cp -rL` dereferences the symlinks into a plain emptyDir, and copying
      # `pips` specifically rather than `.` leaves the dot-directories behind.
      # helm template cannot catch this; only a running pod can.
      initContainers = {
        extraInitContainers = yamlencode([{
          name    = "flatten-protos"
          image   = "busybox:1.36"
          command = ["sh", "-c", "cp -rL /src/pips /out/"]
          volumeMounts = [
            { name = "protos-src", mountPath = "/src" },
            { name = "protos", mountPath = "/out" },
          ]
        }])
      }

      extraVolumeMounts = [{
        name      = "protos"
        mountPath = "/etc/pipsim/proto"
        readOnly  = true
      }]

      config = {
        kafka = {
          protobuf = local.console_protobuf
        }
      }
    }
  })]
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
# TypeScript in one chain. Every service exports to this collector.
#
# grafana/otel-lgtm bundles the OTel Collector with Loki + Grafana + Tempo +
# Mimir/Prometheus in one image, with datasources and trace<->log<->metric
# correlation provisioned out of the box — it replaces the separate Jaeger
# deployment and the hand-rolled collector ConfigMap that used to live here
# (and used to drift from infra/otel/collector.yaml, its compose twin).
# The Service keeps the name `otel-collector` so OTEL_EXPORTER_OTLP_ENDPOINT
# in every chart needs no change.
# ---------------------------------------------------------------------------

resource "kubernetes_config_map" "grafana_dashboards" {
  metadata {
    name      = "pipsim-grafana-dashboards"
    namespace = kubernetes_namespace.platform.metadata[0].name
  }

  data = {
    "pipsim.yaml" = file("${path.module}/../../grafana/dashboards-provisioning.yaml")
    "pipsim.json" = file("${path.module}/../../grafana/dashboards/pipsim.json")
  }
}

resource "kubernetes_deployment" "observability" {
  metadata {
    name      = "otel-lgtm"
    namespace = kubernetes_namespace.platform.metadata[0].name
    labels    = { app = "otel-lgtm" }
  }

  spec {
    replicas = 1
    selector {
      match_labels = { app = "otel-lgtm" }
    }
    template {
      metadata {
        labels = { app = "otel-lgtm" }
        annotations = {
          # Roll the pod when a dashboard changes; a ConfigMap edit alone does
          # not restart anything.
          "pipsim.dev/dashboards-hash" = sha256(join("", values(kubernetes_config_map.grafana_dashboards.data)))
        }
      }
      spec {
        container {
          name  = "otel-lgtm"
          image = "grafana/otel-lgtm:0.30.0"

          port {
            name           = "grafana"
            container_port = 3000
          }
          port {
            name           = "otlp-grpc"
            container_port = 4317
          }
          port {
            name           = "otlp-http"
            container_port = 4318
          }

          # The provisioning YAML must land next to the dashboard JSON it
          # provisions, both under grafana/conf/provisioning/dashboards/pipsim
          # — Grafana's file provider does not look outside that directory.
          volume_mount {
            name       = "dashboards"
            mount_path = "/otel-lgtm/grafana/conf/provisioning/dashboards/pipsim.yaml"
            sub_path   = "pipsim.yaml"
          }
          volume_mount {
            name       = "dashboards"
            mount_path = "/otel-lgtm/grafana/conf/provisioning/dashboards/pipsim/pipsim.json"
            sub_path   = "pipsim.json"
          }

          resources {
            requests = {
              cpu    = "250m"
              memory = "1Gi"
            }
          }
        }

        volume {
          name = "dashboards"
          config_map {
            name = kubernetes_config_map.grafana_dashboards.metadata[0].name
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
    selector = { app = "otel-lgtm" }
    port {
      name        = "grafana"
      port        = 3000
      target_port = 3000
    }
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
