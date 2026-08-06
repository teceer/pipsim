defmodule Broadcast.MixProject do
  use Mix.Project

  def project do
    [
      app: :broadcast,
      version: "0.1.0",
      elixir: "~> 1.17",
      start_permanent: Mix.env() == :prod,
      deps: deps()
    ]
  end

  def application do
    [
      mod: {Broadcast.Application, []},
      extra_applications: [:logger]
    ]
  end

  defp deps do
    [
      {:phoenix, "~> 1.7"},
      {:phoenix_pubsub, "~> 2.1"},
      # Consumes the StreamWorld server stream from world-gateway.
      #
      # 1.0 rather than 0.9, and not for the features: 0.11.5 was the top of the
      # 0.x range and carries EEF-CVE-2026-48853 — remote code execution through
      # unsafe Erlang term deserialisation — plus an authorization bypass in HTTP
      # transcoding. Neither is fixed anywhere in 0.x, so the constraint itself
      # was the vulnerability.
      {:grpc, "~> 1.0"},
      {:protobuf, "~> 0.13"},
      {:jason, "~> 1.4"},
      {:opentelemetry, "~> 1.5"},
      {:opentelemetry_exporter, "~> 1.8"},
      {:opentelemetry_phoenix, "~> 1.2"},
      {:credo, "~> 1.7", only: [:dev, :test], runtime: false}
    ]
  end
end
