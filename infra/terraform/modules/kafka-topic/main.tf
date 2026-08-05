# A Kafka topic with the conventions this project always wants.
#
# Wrapping the resource in a module is not ceremony: it is where the defaults
# that matter — compression, min in-sync replicas, cleanup policy — live in one
# place instead of being copy-pasted per topic and drifting.

terraform {
  required_providers {
    kafka = {
      source  = "Mongey/kafka"
      version = "~> 0.7"
    }
  }
}

variable "name" {
  description = "Topic name, always pipsim.<domain>.<event>.v1"
  type        = string

  validation {
    condition     = can(regex("^pipsim\\.[a-z]+\\.[a-z_]+\\.v[0-9]+$", var.name))
    error_message = "Topic must follow pipsim.<domain>.<event>.v<n>."
  }
}

variable "partitions" {
  description = <<-EOT
    Partition count. This is a one-way door: raising it later reshuffles keys
    across partitions, which breaks per-entity ordering for everything already
    written. Choose it deliberately.
  EOT
  type        = number
}

variable "replication_factor" {
  type    = number
  default = 1
}

variable "retention_ms" {
  description = "-1 means retain forever, which pairs with cleanup.policy=compact."
  type        = number
  default     = 604800000
}

resource "kafka_topic" "this" {
  name               = var.name
  partitions         = var.partitions
  replication_factor = var.replication_factor

  config = {
    "retention.ms" = tostring(var.retention_ms)

    # Infinite retention only makes sense for a topic where the latest value per
    # key is the point. For lifecycle topics the history *is* the data, so they
    # stay on delete.
    "cleanup.policy" = var.retention_ms == -1 ? "compact" : "delete"

    # Protobuf payloads compress well and the CPU cost is irrelevant at this
    # message rate.
    "compression.type" = "zstd"

    "min.insync.replicas" = tostring(min(var.replication_factor, 1))
  }
}

output "name" {
  value = kafka_topic.this.name
}
