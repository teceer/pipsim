import Config

# Read at boot, for both `mix run` and a release — unlike config.exs, this
# file has System.get_env available and is not baked into a compiled build.
config :opentelemetry,
  resource: [service: %{name: "tavern"}]

if endpoint = System.get_env("OTEL_EXPORTER_OTLP_ENDPOINT") do
  config :opentelemetry_exporter,
    otlp_protocol: :grpc,
    otlp_endpoint: endpoint
end
