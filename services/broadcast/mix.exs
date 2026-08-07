defmodule Broadcast.MixProject do
  use Mix.Project

  def project do
    [
      app: :broadcast,
      version: "0.1.0",
      elixir: "~> 1.17",
      start_permanent: Mix.env() == :prod,
      elixirc_paths: elixirc_paths(Mix.env()),
      deps: deps()
    ]
  end

  # The generated contracts live at the repo root, outside this project. The
  # path is expanded because Mix resolves a relative entry pointing outside the
  # project against the build directory and then silently finds no sources —
  # the same trap documented in the tavern.
  @contracts Path.expand("../../gen/elixir", __DIR__)

  defp elixirc_paths(:test), do: ["lib", "test/support", @contracts]
  defp elixirc_paths(_), do: ["lib", @contracts]

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
      # The HTTP adapter Phoenix needs to bind a listener. Bandit rather than
      # Cowboy, matching the tavern — one Elixir HTTP server in the repo is one
      # set of behaviours to know.
      {:bandit, "~> 1.0"},
      # Consumes the StreamWorld server stream from world-gateway.
      #
      # 1.0 rather than 0.9, and not for the features: 0.11.5 was the top of the
      # 0.x range and carries EEF-CVE-2026-48853 — remote code execution through
      # unsafe Erlang term deserialisation — plus an authorization bypass in HTTP
      # transcoding. Neither is fixed anywhere in 0.x, so the constraint itself
      # was the vulnerability.
      {:grpc, "~> 1.0"},
      # grpc declares both of its transports optional and defaults to Gun, so
      # without one of them every connect fails at runtime with an undefined
      # GRPC.Client.Adapters.Gun.connect/2 — a dependency that compiles fine
      # and cannot open a socket.
      #
      # Mint rather than Gun: pure Elixir, no separate connection-pool process
      # tree, and it does not pull cowlib in alongside the Bandit stack this
      # service already runs.
      {:mint, "~> 1.9"},
      {:protobuf, "~> 0.13"},
      {:jason, "~> 1.4"},
      {:opentelemetry, "~> 1.5"},
      {:opentelemetry_exporter, "~> 1.8"},
      {:opentelemetry_phoenix, "~> 1.2"},
      {:credo, "~> 1.7", only: [:dev, :test], runtime: false}
    ]
  end
end
