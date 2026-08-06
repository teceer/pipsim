defmodule Tavern.Buildings do
  @moduledoc """
  Which taverns this process hosts.

  A workplace service owns a *kind* of building, not one building — see ADR
  0005. `WORKPLACES` is the multi-building form (`id:x:y`, comma separated);
  the single `WORKPLACE_ID`/`X`/`Y` triple still works and means exactly one
  tavern, which is what the chart and every local `make run` pass.

  Strict on the multi form on purpose. A typo used to be survivable because
  there was one building and a wrong coordinate merely put it in the wrong
  field; with several, silently dropping one means a building that never
  appears and an economy quietly smaller than the one that was configured.
  """

  require Logger

  @type t :: %{id: non_neg_integer(), x: integer(), y: integer()}

  @doc """
  The configured buildings.

  Application config wins over the environment, so a test or a release can
  declare buildings without depending on what was exported before the VM
  started. Nothing in production sets it, which leaves `WORKPLACES` — and then
  the single-building triple — as the real path.
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

  defp parse_one!(spec) do
    case String.split(spec, ":") do
      [id, x, y] ->
        %{id: int!(id, spec), x: int!(x, spec), y: int!(y, spec)}

      _ ->
        raise ArgumentError, "want id:x:y, got #{inspect(spec)}"
    end
  end

  defp int!(value, spec) do
    case Integer.parse(String.trim(value)) do
      {n, ""} -> n
      _ -> raise ArgumentError, "unparseable number in #{inspect(spec)}"
    end
  end

  # An unset variable and one set to the empty string mean the same thing here.
  # They did not, once: a Helm template rendered WORKPLACE_Y empty and this
  # crashed the release on boot, where the Go services had been quietly falling
  # back to a default for weeks and hiding the same bug.
  defp legacy do
    %{
      id: env_int("WORKPLACE_ID", 2),
      x: env_int("WORKPLACE_X", 32_000),
      y: env_int("WORKPLACE_Y", 20_000)
    }
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
