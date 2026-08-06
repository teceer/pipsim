defmodule Tavern.DaprTest do
  @moduledoc """
  The hand-written half of the Dapr integration, checked without a sidecar.

  What is *not* here: the state store. Exercising `Tavern.Store.Dapr` needs a
  running sidecar, because the only thing it does is talk to one — a fake would
  be asserting that this module can call a mock. The conformance suite driven
  against a real `daprd` is what covers it, and `make test` stays cluster-free.

  What is here is everything the sidecar depends on being exactly right, where
  "wrong" fails a long way from the cause.
  """

  use ExUnit.Case, async: false

  import Plug.Conn
  import Plug.Test

  alias Pips.Workplace.V1.{DescribeRequest, StartShiftRequest}

  @opts Tavern.Connect.init(handler: Tavern.Workplace)

  defp actor(verb, path, body \\ "{}") do
    verb
    |> conn(path, body)
    |> put_req_header("content-type", "application/json")
    |> Tavern.Connect.call(@opts)
  end

  describe "the entity declaration" do
    test "names the actor type the store addresses" do
      conn = :get |> conn("/dapr/config") |> Tavern.Connect.call(@opts)

      assert conn.status == 200
      assert %{"entities" => [entity]} = Jason.decode!(conn.resp_body)

      # Not a literal: if these two ever disagree, the sidecar registers one
      # name and the store writes under another, and every call fails as
      # "actor instance is missing".
      assert entity == Tavern.Dapr.actor_type()
    end
  end

  describe "invocation callbacks" do
    # The asymmetry that cost an afternoon: a caller invokes an actor with POST,
    # and the sidecar then calls the app with PUT. Matching only POST made Dapr
    # report ERR_ACTOR_INVOKE_METHOD "actor method not found", which reads like
    # the entity was never registered.
    test "PUT is accepted, because that is what the sidecar sends" do
      conn = actor(:put, "/actors/tavern/2/method/Describe")

      assert conn.status == 200
      assert %{"workplaceId" => "2"} = Jason.decode!(conn.resp_body)
    end

    test "POST is accepted too" do
      assert actor(:post, "/actors/tavern/2/method/Describe").status == 200
    end

    test "the building comes from the path, not the body" do
      # An empty body is what Dapr sends for a method whose arguments are all
      # defaults, so the id has to be recoverable from the URL alone.
      conn = actor(:put, "/actors/tavern/2/method/Describe", "")

      assert conn.status == 200
      assert %{"workplaceId" => "2"} = Jason.decode!(conn.resp_body)
    end

    test "work reaches the building named in the path" do
      pip = System.unique_integer([:positive])

      started =
        actor(
          :put,
          "/actors/tavern/2/method/StartShift",
          Protobuf.JSON.encode!(%StartShiftRequest{pip_id: pip, tick: 1})
        )

      assert started.status == 200
      assert %{"accepted" => true} = Jason.decode!(started.resp_body)

      assert Tavern.Workplace.describe(%DescribeRequest{workplace_id: 2}).current_workers >= 1
      assert Tavern.Workplace.describe(%DescribeRequest{workplace_id: 4}).current_workers == 0

      Tavern.Store.Process.release(2, pip)
    end

    test "a method outside the contract is unimplemented, not a crash" do
      assert actor(:put, "/actors/tavern/2/method/DropTheTables").status == 501
    end

    test "a building this process does not host is not found" do
      assert actor(:put, "/actors/tavern/999/method/Describe").status == 404
    end

    # Activation and deactivation carry no method and must simply succeed:
    # a building keeps its state in the store, so there is nothing to set up.
    test "lifecycle probes succeed" do
      assert actor(:post, "/actors/tavern/2").status == 200

      assert conn(:delete, "/actors/tavern/2") |> Tavern.Connect.call(@opts) |> Map.get(:status) ==
               200
    end
  end

  describe "the wire form of persisted state" do
    test "survives a round trip through JSON" do
      shift = %Tavern.Shifts.Shift{
        started_tick: 7,
        last_work_tick: 19,
        # Wall clock, deliberately: this value outlives the process that wrote
        # it, and monotonic time has no meaning across a restart.
        last_work_ms: 1_785_000_000_000
      }

      encoded = Jason.encode!(%{"42" => Map.from_struct(shift)})
      assert %{"42" => decoded} = Jason.decode!(encoded)

      assert decoded["started_tick"] == 7
      assert decoded["last_work_tick"] == 19
      assert decoded["last_work_ms"] == 1_785_000_000_000
    end
  end
end

defmodule Tavern.BuildingsTest do
  use ExUnit.Case, async: true

  alias Tavern.Buildings

  test "parses the multi-building form" do
    assert [%{id: 2, x: 32_000, y: 20_000}, %{id: 4, x: 40_000, y: 12_000}] =
             Buildings.parse!(" 2:32000:20000 , 4:40000:12000 ")
  end

  # Strict on purpose: a dropped building is an economy quietly smaller than the
  # one that was configured, and nothing would report it.
  test "refuses anything it cannot read exactly" do
    for bad <- ["", "2:3", "2:3:4:5", "x:1:2", "2:y:3"] do
      assert_raise ArgumentError, fn -> Buildings.parse!(bad) end
    end
  end
end

defmodule Tavern.WorkplaceRoutingTest do
  @moduledoc """
  Routing on `workplace_id`, which is what a service hosting a kind of building
  has to get right. The tests run against whatever buildings the test config
  declares.
  """

  use ExUnit.Case, async: false

  alias Pips.Workplace.V1.{DescribeRequest, ListRequest, StartShiftRequest, WorkRequest}

  test "List reports every building" do
    ids =
      %ListRequest{}
      |> Tavern.Workplace.list()
      |> Map.fetch!(:workplaces)
      |> Enum.map(& &1.workplace_id)

    assert 2 in ids
    assert 4 in ids
  end

  test "buildings do not share seats" do
    pip = System.unique_integer([:positive])

    Tavern.Workplace.start_shift(%StartShiftRequest{workplace_id: 2, pip_id: pip, tick: 1})

    assert Tavern.Workplace.describe(%DescribeRequest{workplace_id: 2}).current_workers >= 1
    assert Tavern.Workplace.describe(%DescribeRequest{workplace_id: 4}).current_workers == 0

    # A pip on shift at one tavern is a stranger at the next, even though both
    # live in this process.
    assert Tavern.Workplace.work(%WorkRequest{workplace_id: 4, pip_id: pip, tick: 2}).shift_should_end

    Tavern.Store.Process.release(2, pip)
  end

  test "Describe with no id is refused while hosting several" do
    assert catch_throw(Tavern.Workplace.describe(%DescribeRequest{})) ==
             {:connect_error, :invalid_argument}
  end

  test "a building this process does not host is not found" do
    assert catch_throw(Tavern.Workplace.describe(%DescribeRequest{workplace_id: 999})) ==
             {:connect_error, :not_found}
  end
end
