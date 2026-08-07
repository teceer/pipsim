import Config

# The listener stays down under `mix test`, the same way the tavern's does: a
# suite that binds a port fails whenever anything else on the machine holds it,
# and Phoenix.ChannelTest drives the socket directly without one.
config :broadcast, start_server: false
config :broadcast, Broadcast.Endpoint, server: false

# No gateway either. The suite tests fan-out given a delta, not the ability to
# reach a running cluster — `make test` must never need one.
config :broadcast, gateway_url: nil

config :logger, level: :warning
