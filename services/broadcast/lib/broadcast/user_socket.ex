defmodule Broadcast.UserSocket do
  @moduledoc """
  One socket per browser, carrying any number of cell subscriptions.

  There is no authentication. This service is read-only by design — see
  `CLAUDE.md`: player actions go to world-gateway over Connect, never through a
  channel here — so a connection grants the ability to watch a world that is
  already public to anyone who can reach the gateway. When there is something
  worth protecting, this is where a token would be checked, and the fact that
  `connect/3` currently ignores its params is the marker for it.
  """

  use Phoenix.Socket

  channel("world:cell:*", Broadcast.WorldChannel)

  @impl true
  def connect(_params, socket, _connect_info), do: {:ok, socket}

  # Anonymous: nothing addresses a specific viewer, and giving every socket the
  # same id would let one client's disconnect drop every other client's socket,
  # which is what `Endpoint.broadcast("user_socket:" <> id, "disconnect", %{})`
  # does.
  @impl true
  def id(_socket), do: nil
end
