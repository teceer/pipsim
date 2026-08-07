defmodule Tavern.ConnectTest do
  @moduledoc """
  The wire protocol, exercised as a caller sees it.

  Hand-rolling a protocol is only defensible if it is tested as a protocol
  rather than as a function call, so these go through the plug with real
  encoded bodies and check the paths, the content types and the failure modes —
  not just the happy path a mock would have flattered.
  """

  use ExUnit.Case, async: false
  import Plug.Conn
  import Plug.Test

  alias Pips.Workplace.V1.{DescribeRequest, DescribeResponse, StartShiftRequest}

  @opts Tavern.Connect.init(handler: Tavern.Workplace)
  @service "/pips.workplace.v1.WorkplaceService"

  defp post(path, body, content_type) do
    :post
    |> conn(path, body)
    |> put_req_header("content-type", content_type)
    |> Tavern.Connect.call(@opts)
  end

  test "a protobuf request comes back as protobuf" do
    body = Protobuf.encode(%DescribeRequest{workplace_id: 2})
    conn = post("#{@service}/Describe", body, "application/proto")

    assert conn.status == 200

    assert get_resp_header(conn, "content-type") == ["application/proto"],
           "connect-go compares this by exact string; a charset breaks interop"

    describe = Protobuf.decode(conn.resp_body, DescribeResponse)
    assert describe.kind == "tavern"
    assert describe.max_workers == Tavern.Shifts.max_workers()
    assert describe.produces == [:RESOURCE_KIND_ALE]
  end

  # The reason Connect was chosen over gRPC in ADR 0003 is that this works at
  # all. If it stops working, `curl` stops being a debugger for this service.
  test "a JSON request comes back as JSON" do
    conn = post("#{@service}/Describe", ~s({"workplaceId":"2"}), "application/json")

    assert conn.status == 200
    assert %{"kind" => "tavern"} = Jason.decode!(conn.resp_body)
  end

  test "an unknown method is unimplemented, not a crash" do
    body = Protobuf.encode(%DescribeRequest{})
    conn = post("#{@service}/Carouse", body, "application/proto")

    assert conn.status == 501
    assert %{"code" => "unimplemented"} = Jason.decode!(conn.resp_body)
  end

  test "another service's path is not served here" do
    body = Protobuf.encode(%DescribeRequest{})
    conn = post("/pips.sim.v1.SimService/Snapshot", body, "application/proto")

    assert conn.status == 501
  end

  test "a body that is not the expected message is rejected, not guessed at" do
    conn = post("#{@service}/StartShift", <<0xFF, 0xFF, 0xFF, 0xFF>>, "application/proto")

    assert conn.status == 400
    assert %{"code" => "invalid_argument"} = Jason.decode!(conn.resp_body)
  end

  test "healthz reports the headcount, like every other service" do
    conn = :get |> conn("/healthz") |> Tavern.Connect.call(@opts)

    assert conn.status == 200
    assert %{"status" => "ok", "workers" => _} = Jason.decode!(conn.resp_body)
  end

  test "a shift can be started over the wire and shows up in Describe" do
    pip = System.unique_integer([:positive])

    start =
      post(
        "#{@service}/StartShift",
        Protobuf.encode(%StartShiftRequest{workplace_id: 2, pip_id: pip, tick: 1}),
        "application/proto"
      )

    assert start.status == 200

    before = Tavern.Workplace.describe(%DescribeRequest{workplace_id: 2})
    assert before.current_workers >= 1

    # Addressed by building now: shifts live in a process per tavern, reached
    # through the registry rather than under one global name.
    Tavern.Store.Process.release(2, pip)
  end
end
