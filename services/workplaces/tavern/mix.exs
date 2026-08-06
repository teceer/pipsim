defmodule Tavern.MixProject do
  use Mix.Project

  def project do
    [
      app: :tavern,
      version: "0.1.0",
      elixir: "~> 1.17",
      start_permanent: Mix.env() == :prod,
      # Contracts are generated once, centrally, and committed — the same
      # gen/ tree Go and TypeScript read. Compiling them in from there is what
      # keeps "one source of truth for contracts" true across six languages
      # rather than true for the convenient ones.
      elixirc_paths: elixirc_paths(Mix.env()),
      deps: deps()
    ]
  end

  def application do
    [
      mod: {Tavern.Application, []},
      extra_applications: [:logger]
    ]
  end

  # Expanded, not relative: Mix resolves a relative entry against the *build*
  # directory for anything outside the project root, and silently fails to find
  # the sources.
  @contracts Path.expand("../../../gen/elixir", __DIR__)

  defp elixirc_paths(:test), do: ["lib", "test/support", @contracts]
  defp elixirc_paths(_), do: ["lib", @contracts]

  defp deps do
    [
      # No gRPC library, on purpose.
      #
      # The gateway calls workplaces over the *Connect* protocol, and unary
      # Connect is an ordinary HTTP POST to /<package>.<Service>/<Method> with a
      # bare protobuf body — no framing, no HTTP/2 requirement. Plug and Bandit
      # answer that in a few dozen lines, where elixir-grpc would drag in its
      # own codegen conventions to speak a protocol nobody here is asking for.
      {:bandit, "~> 1.5"},
      {:plug, "~> 1.16"},
      {:protobuf, "~> 0.13"},
      {:jason, "~> 1.4"},

      # Competing consumers on pipsim.work.tavern, same as the farm.
      {:amqp, "~> 4.0"},
      {:credo, "~> 1.7", only: [:dev, :test], runtime: false}
    ]
  end
end
