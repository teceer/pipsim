# Layer 20 — topics, exchanges, schemas and roles.
#
# This is the layer that justifies Terraform in this project. Partition counts,
# retention and ACLs live in version control and show up in a `terraform plan`
# before anything changes, instead of being whatever someone once typed into
# `rpk topic create`.
#
# Requires layer 10 to be RUNNING, not merely applied — these providers open
# connections at plan time. That is why the root Makefile waits on a readiness
# probe between the two.

terraform {
  required_version = ">= 1.9"

  required_providers {
    kafka = {
      source  = "Mongey/kafka"
      version = "~> 0.7"
    }
    rabbitmq = {
      source  = "cyrilgdn/rabbitmq"
      version = "~> 1.8"
    }
    postgresql = {
      source  = "cyrilgdn/postgresql"
      version = "~> 1.23"
    }
  }

  backend "local" {
    path = "terraform.tfstate"
  }
}

variable "kafka_bootstrap" {
  type    = string
  default = "localhost:9092"
}

variable "rabbitmq_endpoint" {
  type    = string
  default = "http://localhost:15672"
}

variable "postgres_host" {
  type    = string
  default = "localhost"
}

provider "kafka" {
  bootstrap_servers = [var.kafka_bootstrap]
  tls_enabled       = false
}

provider "rabbitmq" {
  endpoint = var.rabbitmq_endpoint
  username = "pipsim"
  password = "pipsim"
}

provider "postgresql" {
  host     = var.postgres_host
  port     = 5432
  username = "postgres"
  password = "pipsim"
  sslmode  = "disable"
}

# ---------------------------------------------------------------------------
# Kafka — the immutable fact log.
#
# Naming: pipsim.<domain>.<event>.v1
# Partition key is always the aggregate id, so one entity's events keep their
# relative order. That is why partition counts here are a real decision and not
# a default: raising them later reshuffles keys across partitions and breaks
# per-entity ordering for everything already written.
# ---------------------------------------------------------------------------

locals {
  topics = {
    "pipsim.pip.lifecycle.v1" = {
      partitions = 6
      # Seven days: long enough to replay a week of a world, short enough that
      # the laptop survives it.
      retention_ms = 604800000
    }
    "pipsim.pip.work.v1" = {
      partitions   = 6
      retention_ms = 604800000
    }
    "pipsim.economy.resources.v1" = {
      partitions   = 3
      retention_ms = 604800000
    }
    "pipsim.world.buildings.v1" = {
      # Compacted: only the latest state of each building matters for rebuilding
      # a projection, unlike the lifecycle topics where the history *is* the data.
      partitions   = 3
      retention_ms = -1
    }
  }
}

module "topics" {
  source   = "../modules/kafka-topic"
  for_each = local.topics

  name              = each.key
  partitions        = each.value.partitions
  retention_ms      = each.value.retention_ms
  replication_factor = 1
}

# ---------------------------------------------------------------------------
# RabbitMQ — task distribution.
#
# Distinct from Kafka by semantics, not by taste: here a message is consumed by
# exactly one worker and acknowledged individually. "Five pips want work at this
# workshop, first one to ack gets it."
# ---------------------------------------------------------------------------

resource "rabbitmq_vhost" "pipsim" {
  name = "pipsim"
}

resource "rabbitmq_exchange" "work" {
  name  = "pipsim.work"
  vhost = rabbitmq_vhost.pipsim.name

  settings {
    type        = "topic"
    durable     = true
    auto_delete = false
  }
}

resource "rabbitmq_queue" "workplace_tasks" {
  for_each = toset(["farm", "workshop", "tavern"])

  name  = "pipsim.work.${each.key}"
  vhost = rabbitmq_vhost.pipsim.name

  settings {
    durable     = true
    auto_delete = false
    arguments = {
      # Dead-letter rather than silently dropping: a task nobody can perform is
      # a bug worth seeing, not noise worth hiding.
      "x-dead-letter-exchange" = "pipsim.work.dlx"
    }
  }
}

resource "rabbitmq_exchange" "work_dlx" {
  name  = "pipsim.work.dlx"
  vhost = rabbitmq_vhost.pipsim.name

  settings {
    type        = "fanout"
    durable     = true
    auto_delete = false
  }
}

# ---------------------------------------------------------------------------
# Postgres — one schema and one role per service.
#
# This is the boundary that decides whether this is a microservice architecture
# or a distributed monolith. No service may read another's tables; cross-service
# reads go through RPC or Kafka.
# ---------------------------------------------------------------------------

locals {
  service_databases = [
    "world_gateway",
    "farm",
    "workshop",
    "tavern",
    "bff",
  ]
}

resource "postgresql_role" "service" {
  for_each = toset(local.service_databases)

  name     = each.key
  login    = true
  password = each.key
}

resource "postgresql_database" "service" {
  for_each = toset(local.service_databases)

  name              = each.key
  owner             = postgresql_role.service[each.key].name
  lc_collate        = "C"
  connection_limit  = 20
  allow_connections = true
}
