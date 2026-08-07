defmodule Broadcast.WorldChannel do
  @moduledoc """
  One topic per world cell. A client joins the cells its viewport covers and
  leaves them as it pans — ADR 0010 decision 2.

  The channel holds no state about the world. It subscribes, and the fan-out
  process pushes; that is what makes a cell's cost one message per push
  regardless of how many browsers are watching it.
  """

  use Phoenix.Channel
  require Logger

  @impl true
  def join(topic, _params, socket) do
    case Broadcast.Grid.parse_topic(topic) do
      {:ok, cell} ->
        # Nothing is replayed on join. A client needs a full picture to start
        # from, and it gets one from JoinWorld on the gateway — the same
        # snapshot it uses to recover from a gap. Pushing the last delta here
        # would give it a partial world that looks complete.
        {:ok, assign(socket, :cell, cell)}

      :error ->
        # A malformed topic is a client bug, not an attack to log loudly, but
        # it must not become a subscription to a cell nobody can compute.
        {:error, %{reason: "not a world cell"}}
    end
  end

  # Read-only, and this is the enforcement rather than a convention. Player
  # actions belong on the gateway's Connect API; accepting them here would put
  # a write path through a service that is not allowed to have one.
  @impl true
  def handle_in(event, _payload, socket) do
    Logger.warning("ignoring inbound event on a read-only channel", event: event)
    {:reply, {:error, %{reason: "broadcast is read-only"}}, socket}
  end
end
