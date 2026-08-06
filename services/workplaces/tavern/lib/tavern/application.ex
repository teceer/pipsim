defmodule Tavern.Application do
  @moduledoc false

  use Application
  require Logger

  @impl true
  def start(_type, _args) do
    load_config()

    children = [{Tavern.Shifts, name: Tavern.Shifts}] ++ server() ++ offers()

    Logger.info("tavern listening",
      port: port(),
      workplace_id: Application.fetch_env!(:tavern, :workplace_id),
      max_workers: Tavern.Shifts.max_workers()
    )

    Supervisor.start_link(children, strategy: :one_for_one, name: Tavern.Supervisor)
  end

  defp server do
    if Application.get_env(:tavern, :start_server, true) do
      [{Bandit, plug: {Tavern.Connect, handler: Tavern.Workplace}, port: port(), scheme: :http}]
    else
      []
    end
  end

  # Without a broker URL the tavern still serves its RPCs and simply never
  # receives offers — the same shape as the farm, so `make dev` works with or
  # without RabbitMQ.
  defp offers do
    case System.get_env("AMQP_URL") do
      nil ->
        Logger.info("no AMQP_URL set; not consuming work offers")
        []

      "" ->
        []

      url ->
        [{Tavern.Offers, url: url}]
    end
  end

  # Position and id are configuration for now, exactly as they are for the farm.
  # Once BuildWorkplace works, both arrive from the player action that created
  # the building.
  defp load_config do
    Application.put_env(:tavern, :workplace_id, env_int("WORKPLACE_ID", 2))
    Application.put_env(:tavern, :x, env_int("WORKPLACE_X", 32_000))
    Application.put_env(:tavern, :y, env_int("WORKPLACE_Y", 20_000))
  end

  defp port, do: env_int("PORT", 8090)

  # An unset variable and one set to the empty string mean the same thing here.
  # They did not, once: a Helm template rendered WORKPLACE_Y empty and this
  # crashed the release on boot, where the Go services had been quietly falling
  # back to a default for weeks and hiding the same bug.
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
