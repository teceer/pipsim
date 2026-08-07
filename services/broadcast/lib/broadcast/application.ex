defmodule Broadcast.Application do
  @moduledoc """
  Root supervisor.

  `:one_for_one`, and that is the whole point of the service being on the BEAM:
  a crashed connection must never take the fan-out down, and the fan-out
  restarting must not disturb the connections. See ADR 0010.

  Order matters. PubSub starts first because both the endpoint's channels and
  the gateway client publish through it; the gateway client starts last so it
  never pushes into a PubSub that is not there yet.
  """

  use Application
  require Logger

  @impl true
  def start(_type, _args) do
    children =
      [{Phoenix.PubSub, name: Broadcast.PubSub}] ++
        endpoint() ++
        gateway_client()

    Logger.info("broadcast starting",
      port: Application.get_env(:broadcast, :port, 4000),
      gateway: Application.get_env(:broadcast, :gateway_url, "(none)"),
      cell_tiles: Broadcast.Grid.tiles_per_cell()
    )

    Supervisor.start_link(children, strategy: :one_for_one, name: Broadcast.Supervisor)
  end

  # Off under `mix test`, for the same reason the tavern's listener is: a suite
  # that binds port 4000 fails whenever anything else on the machine holds it,
  # and the channel tests drive the socket directly through
  # Phoenix.ChannelTest rather than over a real connection.
  defp endpoint do
    if Application.get_env(:broadcast, :start_server, true) do
      [Broadcast.Endpoint]
    else
      []
    end
  end

  # Absent without a gateway configured, so the service still boots and serves
  # channels when there is nothing upstream — which is what `mix test` and a
  # lone `mix phx.server` both do.
  defp gateway_client do
    case Application.get_env(:broadcast, :gateway_url) do
      nil -> []
      url -> [{Broadcast.GatewayClient, url: url}]
    end
  end

  @impl true
  def config_change(changed, _new, removed) do
    Broadcast.Endpoint.config_change(changed, removed)
    :ok
  end
end
