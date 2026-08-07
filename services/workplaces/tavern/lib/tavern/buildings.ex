defmodule Tavern.Buildings do
  @moduledoc """
  Which taverns this process hosts.

  A workplace service owns a *kind* of building, not one building — see ADR
  0005. `WORKPLACES` is the multi-building form (comma-separated ids); the
  single `WORKPLACE_ID` variable still works and means exactly one tavern,
  which is what the chart and every local `make run` pass.

  Just ids: where a building stands is not this service's business, only
  sim-core's — see ADR 0008. The gateway supplies position separately when it
  registers the building.

  Strict on the multi form on purpose. A typo used to be survivable because
  there was one building; with several, silently dropping one means a
  building that never appears and an economy quietly smaller than the one
  that was configured.
  """

  require Logger

  @type t :: %{id: non_neg_integer()}

  @doc """
  The configured buildings.

  Application config wins over the environment, so a test or a release can
  declare buildings without depending on what was exported before the VM
  started. Nothing in production sets it, which leaves `WORKPLACES` — and then
  the single-building `WORKPLACE_ID` — as the real path.
  """
  @spec load() :: [t()]
  def load do
    case Application.get_env(:tavern, :buildings) do
      [_ | _] = configured -> configured
      _ -> from_env()
    end
  end

  defp from_env do
    case System.get_env("WORKPLACES") do
      nil -> [legacy()]
      "" -> [legacy()]
      raw -> parse!(raw)
    end
  end

  @spec parse!(String.t()) :: [t()]
  def parse!(raw) do
    buildings =
      raw
      |> String.split(",")
      |> Enum.map(&String.trim/1)
      |> Enum.reject(&(&1 == ""))
      |> Enum.map(&parse_one!/1)

    if buildings == [], do: raise(ArgumentError, "WORKPLACES configured no buildings")
    buildings
  end

  defp parse_one!(spec), do: %{id: int!(spec, spec)}

  defp int!(value, spec) do
    case Integer.parse(String.trim(value)) do
      {n, ""} -> n
      _ -> raise ArgumentError, "unparseable number in #{inspect(spec)}"
    end
  end

  # An unset variable and one set to the empty string mean the same thing here.
  defp legacy do
    %{id: env_int("WORKPLACE_ID", 2)}
  end

  defp env_int(key, default) do
    case System.get_env(key) do
      nil ->
        default

      "" ->
        default

      value ->
        case Integer.parse(value) do
          {n, ""} ->
            n

          _ ->
            Logger.warning("ignoring unparseable value", key: key, value: value)
            default
        end
    end
  end
end
