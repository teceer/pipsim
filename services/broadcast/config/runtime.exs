import Config

# Runtime rather than build time, so one image serves every environment — the
# same reason the other services read their addresses from the environment.
if config_env() != :test do
  port = String.to_integer(System.get_env("PORT") || "4000")

  config :broadcast, Broadcast.Endpoint, http: [ip: {0, 0, 0, 0}, port: port]
  config :broadcast, port: port

  # WORLD_GATEWAY_ADDR is what compose and the Helm chart already set for this
  # service. Absent, the node serves channels and never receives a delta, which
  # is a degraded mode rather than a failure to boot: browsers stay connected
  # and resync through the gateway when it returns.
  config :broadcast, gateway_url: System.get_env("WORLD_GATEWAY_ADDR")
end
