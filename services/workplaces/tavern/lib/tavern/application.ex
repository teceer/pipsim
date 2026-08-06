defmodule Tavern.Application do
  @moduledoc false

  use Application
  require Logger

  @impl true
  def start(_type, _args) do
    load_config()

    children = shift_state() ++ server() ++ offers()

    Logger.info("tavern listening",
      port: port(),
      buildings: Enum.map(buildings(), & &1.id),
      store: inspect(Application.fetch_env!(:tavern, :store)),
      max_workers: Tavern.Shifts.max_workers()
    )

    Supervisor.start_link(children, strategy: :one_for_one, name: Tavern.Supervisor)
  end

  # A GenServer per building, addressed through a Registry — or nothing at all
  # under Dapr, where the state lives in the actor store and the sidecar
  # provides the exclusion the process was providing.
  #
  # Worth noticing what is being replaced. A process per building under a
  # supervisor *is* the actor model, and it is better at everything except the
  # one thing that matters here: it dies with the node. Dapr buys placement,
  # and pays a round trip for it.
  defp shift_state do
    case Application.fetch_env!(:tavern, :store) do
      Tavern.Store.Dapr ->
        []

      Tavern.Store.Process ->
        [{Registry, keys: :unique, name: Tavern.Store.Process.registry()}] ++
          Enum.map(buildings(), fn b ->
            Supervisor.child_spec(
              {Tavern.Shifts, name: Tavern.Store.Process.via(b.id)},
              id: {Tavern.Shifts, b.id}
            )
          end)
    end
  end

  defp server do
    if Application.get_env(:tavern, :start_server, true) do
      handler =
        case Application.fetch_env!(:tavern, :store) do
          Tavern.Store.Dapr -> Tavern.Workplace.Actor
          Tavern.Store.Process -> Tavern.Workplace
        end

      [{Bandit, plug: {Tavern.Connect, handler: handler}, port: port(), scheme: :http}]
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

  # Ids and positions are configuration for now, exactly as they are for the
  # farm. Once BuildWorkplace works, both arrive from the player action that
  # created the building.
  #
  # The store is chosen by whether a sidecar injected DAPR_HTTP_PORT, so there
  # is no separate flag to fall out of step with reality.
  defp load_config do
    Application.put_env(:tavern, :buildings, Tavern.Buildings.load())

    store = if Tavern.Dapr.sidecar(), do: Tavern.Store.Dapr, else: Tavern.Store.Process
    Application.put_env(:tavern, :store, store)
  end

  defp buildings, do: Application.fetch_env!(:tavern, :buildings)

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
