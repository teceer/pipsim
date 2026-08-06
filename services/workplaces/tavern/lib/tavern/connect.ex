defmodule Tavern.Connect do
  @moduledoc """
  The Connect protocol, unary, in about as much code as it deserves.

  This is the whole thing: `POST /<proto.package>.<Service>/<Method>` with a
  bare protobuf message as the body and `Content-Type: application/proto`. No
  length-prefix framing, no trailers, no HTTP/2 requirement. Errors are a JSON
  body with a `code` field and a matching HTTP status.

  Worth stating plainly, because it is the argument for Connect in ADR 0003:
  the reason the browser can call this stack without a proxy is the same reason
  a language with no gRPC story can serve it in one file. gRPC would have
  required HTTP/2 framing and a code generator with opinions; this required
  `Plug`.

  JSON requests (`application/json`) are accepted too, which is what makes
  `curl` a usable debugger against any service in the repo.
  """

  @behaviour Plug

  import Plug.Conn
  require OpenTelemetry.Tracer, as: Tracer

  @proto "application/proto"
  @json "application/json"

  @impl true
  def init(opts), do: opts

  @impl true
  # Two segments, because the service name contains dots but not slashes:
  # /pips.workplace.v1.WorkplaceService/Describe
  #
  # Span named pipsim.tavern.<method>, matching the pipsim.<kind>.<operation>
  # convention every workplace's CLAUDE.md requires. This is the one hop that
  # would otherwise have no code emitting it: Go gets otelconnect and Rust gets
  # tracing-opentelemetry, but a hand-rolled Plug has to say so itself.
  def call(%Plug.Conn{method: "POST", path_info: [service, method]} = conn, opts) do
    Tracer.with_span "pipsim.tavern.#{method}" do
      handler = Keyword.fetch!(opts, :handler)

      with true <- service == handler.service_name() || {:error, :unimplemented},
           {:ok, {request_module, response_module, fun}} <- handler.route(method),
           {:ok, body, conn} <- read_body(conn),
           {:ok, message} <- decode(body, request_module, content_type(conn)) do
        reply(conn, response_module, fun.(message))
      else
        {:error, reason} -> error(conn, reason)
        false -> error(conn, :unimplemented)
      end
    end
  catch
    # Handlers signal a routing failure by throwing rather than by threading an
    # error tuple through six functions that have nothing to say about it. The
    # cases are real: no such building here, and no building named when this
    # process hosts several.
    {:connect_error, reason} -> error(conn, reason)
  end

  # --- what the Dapr sidecar calls back into ---------------------------------
  #
  # Disjoint from the Connect paths, so both contracts live on one port. Served
  # unconditionally: without a sidecar nothing ever calls them.

  # Polled at startup to learn which entities this app hosts. Get it wrong and
  # actors are never registered, which fails later as "actor instance is
  # missing", a long way from the cause.
  def call(%Plug.Conn{method: "GET", path_info: ["dapr", "config"]} = conn, _opts) do
    conn
    |> put_resp_content_type(@json)
    |> send_resp(200, Jason.encode!(Tavern.Dapr.config()))
  end

  # An invocation. This is where work legally touches state: the sidecar routed
  # the call, so this process is authoritative for the building.
  #
  # PUT, not POST. The two directions of the actor API do not agree: a caller
  # invokes an actor with POST, and the sidecar then calls the *app* with PUT.
  # Matching on POST here cost an afternoon — Dapr reports the app's 404 back to
  # the caller as ERR_ACTOR_INVOKE_METHOD "actor method not found", which reads
  # like the entity was never registered. Both are accepted below rather than
  # only PUT, because the asymmetry is not documented anywhere near the callback.
  def call(
        %Plug.Conn{method: verb, path_info: ["actors", _type, id, "method", method]} = conn,
        _opts
      )
      when verb in ["PUT", "POST"] do
    with {:ok, {request_module, response_module, fun}} <- Tavern.Workplace.route(method),
         {:ok, body, conn} <- read_body(conn),
         {:ok, message} <- decode_actor(body, request_module, id) do
      conn
      |> put_resp_content_type(@json)
      |> send_resp(
        200,
        Protobuf.JSON.encode!(struct(response_module, Map.from_struct(fun.(message))))
      )
    else
      {:error, reason} -> error(conn, reason)
    end
  catch
    {:connect_error, reason} -> error(conn, reason)
  end

  # Activation and deactivation carry no method. Nothing to do either way: a
  # building keeps its state in the store, so there is nothing in memory to
  # build up or tear down.
  def call(%Plug.Conn{path_info: ["actors", _type, _id]} = conn, _opts) do
    send_resp(conn, 200, "")
  end

  def call(%Plug.Conn{method: "GET", request_path: "/healthz"} = conn, opts) do
    handler = Keyword.fetch!(opts, :handler)

    conn
    |> put_resp_content_type(@json)
    |> send_resp(200, Jason.encode!(%{status: "ok", workers: handler.workers()}))
  end

  def call(conn, _opts), do: error(conn, :not_found)

  # The building comes from the URL, not the body: Dapr addresses the actor and
  # the request may legitimately not name it.
  defp decode_actor(body, module, id) do
    with {:ok, message} <- decode(body, module, :json) do
      {:ok, %{message | workplace_id: String.to_integer(id)}}
    end
  rescue
    _ -> {:error, :invalid_argument}
  end

  defp content_type(conn) do
    case get_req_header(conn, "content-type") do
      [ct | _] -> if String.starts_with?(ct, @json), do: :json, else: :proto
      [] -> :proto
    end
  end

  defp decode(body, module, :proto) do
    {:ok, Protobuf.decode(body, module)}
  rescue
    _ -> {:error, :invalid_argument}
  end

  defp decode("", module, :json), do: {:ok, struct(module)}

  defp decode(body, module, :json) do
    {:ok, Protobuf.JSON.decode!(body, module)}
  rescue
    _ -> {:error, :invalid_argument}
  end

  # No charset parameter. connect-go compares the content type by exact string
  # and rejects "application/proto; charset=utf-8", which is what
  # put_resp_content_type/2 would send — found by driving this server with the
  # real Go client rather than by reading the spec.
  defp reply(conn, module, message) do
    case content_type(conn) do
      :json ->
        conn
        |> put_resp_content_type(@json, nil)
        |> send_resp(200, Protobuf.JSON.encode!(message))

      :proto ->
        conn
        |> put_resp_content_type(@proto, nil)
        |> send_resp(200, Protobuf.encode(struct(module, Map.from_struct(message))))
    end
  end

  @statuses %{
    invalid_argument: {400, "invalid_argument"},
    unavailable: {503, "unavailable"},
    not_found: {404, "not_found"},
    unimplemented: {501, "unimplemented"},
    internal: {500, "internal"}
  }

  defp error(conn, {:unavailable, _detail}), do: error(conn, :unavailable)

  defp error(conn, reason) do
    {status, code} = Map.get(@statuses, reason, @statuses.internal)

    conn
    |> put_resp_content_type(@json)
    |> send_resp(status, Jason.encode!(%{code: code, message: to_string(code)}))
  end
end
