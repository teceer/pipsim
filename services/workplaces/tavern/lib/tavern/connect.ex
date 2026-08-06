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

  @proto "application/proto"
  @json "application/json"

  @impl true
  def init(opts), do: opts

  @impl true
  # Two segments, because the service name contains dots but not slashes:
  # /pips.workplace.v1.WorkplaceService/Describe
  def call(%Plug.Conn{method: "POST", path_info: [service, method]} = conn, opts) do
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

  def call(%Plug.Conn{method: "GET", request_path: "/healthz"} = conn, opts) do
    handler = Keyword.fetch!(opts, :handler)

    conn
    |> put_resp_content_type(@json)
    |> send_resp(200, Jason.encode!(%{status: "ok", workers: handler.workers()}))
  end

  def call(conn, _opts), do: error(conn, :not_found)

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
    not_found: {404, "not_found"},
    unimplemented: {501, "unimplemented"},
    invalid_argument: {400, "invalid_argument"},
    internal: {500, "internal"}
  }

  defp error(conn, reason) do
    {status, code} = Map.get(@statuses, reason, @statuses.internal)

    conn
    |> put_resp_content_type(@json)
    |> send_resp(status, Jason.encode!(%{code: code, message: to_string(code)}))
  end
end
