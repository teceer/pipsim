import Config

# JSON logs with the shared schema, because every service in this repo emits
# the same shape whatever the language — see ADR 0003. Elixir's default
# console formatter would break that for one service and make the aggregate
# log unreadable for all of them.
config :logger, :default_handler, formatter: {Tavern.LogFormatter, []}

config :logger, level: :info

# :all rather than an enumerated list. The formatter above serialises whatever
# metadata a call site passes, so enumerating keys here would only create a
# second place to forget to update — and a log line that silently drops a field
# is worse than one with an unexpected field in it.
config :logger, metadata: :all

# Whether to bind the HTTP listener at boot.
#
# Off under `mix test`: the suite calls the plug directly, and a test run that
# grabs port 8090 fails whenever anything else on the machine already has it —
# which is exactly what happened the first time this was run next to a farm.
config :tavern, start_server: true

# Batched OTLP export, matching every other service in the repo — see
# crates/server/src/telemetry.rs for the Rust equivalent. The endpoint itself
# is environment-specific, so it is set in runtime.exs, not here.
config :opentelemetry,
  span_processor: :batch,
  traces_exporter: :otlp

import_config "#{config_env()}.exs"
