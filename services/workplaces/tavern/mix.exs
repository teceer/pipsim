defmodule Tavern.MixProject do
  use Mix.Project

  def project do
    [
      app: :tavern,
      version: "0.1.0",
      elixir: "~> 1.17",
      start_permanent: Mix.env() == :prod,
      deps: deps()
    ]
  end

  def application do
    [
      mod: {Tavern.Application, []},
      extra_applications: [:logger]
    ]
  end

  defp deps do
    [
      # Implements pips.workplace.v1.WorkplaceService — produces ALE and is the
      # only workplace that restores the social need rather than draining it.
      {:grpc, "~> 0.9"},
      {:protobuf, "~> 0.13"},
      {:jason, "~> 1.4"},
      {:opentelemetry, "~> 1.5"},
      {:opentelemetry_exporter, "~> 1.8"},
      {:credo, "~> 1.7", only: [:dev, :test], runtime: false}
    ]
  end
end
