import Config

# JSON logs with the shared schema, because every service in this repo emits
# the same shape whatever the language — see ADR 0003.
config :logger, :default_handler, formatter: {Broadcast.LogFormatter, []}
config :logger, level: :info

# :all rather than an enumerated list: the formatter serialises whatever a call
# site passes, so a list here would only be a second place to forget to update.
config :logger, metadata: :all

config :broadcast, Broadcast.Endpoint,
  url: [host: "localhost"],
  adapter: Bandit.PhoenixAdapter,
  http: [ip: {0, 0, 0, 0}, port: 4000],
  # Deltas are ephemeral state and every node holds its own upstream, so there
  # is nothing here worth signing a session cookie over. Phoenix requires the
  # key to boot; it is not protecting anything and is not a secret.
  secret_key_base: String.duplicate("broadcast-has-no-sessions", 3),
  server: true,
  pubsub_server: Broadcast.PubSub

config :phoenix, :json_library, Jason

import_config "#{config_env()}.exs"
