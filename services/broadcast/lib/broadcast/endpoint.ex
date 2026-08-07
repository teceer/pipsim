defmodule Broadcast.Endpoint do
  @moduledoc """
  The socket clients connect to, plus `/healthz`.

  No router and no controllers: this service serves exactly one thing over
  HTTP, and a Phoenix router for a single static response would be scaffolding
  around a two-line plug.
  """

  use Phoenix.Endpoint, otp_app: :broadcast

  socket("/socket", Broadcast.UserSocket,
    websocket: [
      # No custom serializer, despite ADR 0010 decision 5 calling for one.
      #
      # The decision was to keep protobuf bytes off the JSON path; the reason
      # given was that Phoenix's default serializer would force a decode/encode
      # round trip per client per tick. That is no longer true of the default:
      # Phoenix's V2 serializer already fastlanes a `{:binary, data}` payload
      # straight into a binary frame, untouched. Publishing deltas in that
      # shape gets exactly what the ADR asked for, and a hand-written
      # serializer would only be a second implementation of the framing
      # protocol to keep in step with the client library.
      #
      # What the ADR wanted: no re-encoding. What it assumed it needed: our own
      # serializer. Only the first is a requirement.
      max_frame_size: 1_000_000,
      timeout: 60_000
    ],
    longpoll: false
  )

  plug(:healthz)

  # Every service in this repo answers /healthz, whatever the language.
  # Liveness only: it reports that the BEAM is up and serving, not that the
  # gateway upstream is connected. A broadcast node with a dead upstream is
  # still correctly serving the last state it has, and marking it unhealthy
  # would take it out of rotation for a fault that is not its own.
  def healthz(%Plug.Conn{request_path: "/healthz"} = conn, _opts) do
    conn
    |> Plug.Conn.put_resp_content_type("application/json")
    |> Plug.Conn.send_resp(200, ~s({"status":"ok"}))
    |> Plug.Conn.halt()
  end

  def healthz(conn, _opts), do: conn
end
