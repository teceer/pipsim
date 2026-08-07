defmodule Tavern.Workplace.Actor do
  @moduledoc """
  The Connect handler when a sidecar is present: an adapter, and nothing else.

  Every call is handed to the building's actor through the sidecar and the
  reply is passed back. The work itself happens in `Tavern.Workplace`, reached
  from the other side by `Tavern.Connect`'s `/actors/` route.

  That loop looks wasteful and is not optional: Dapr only lets state be touched
  inside an invocation it routed, so the trip out and back is what earns the
  right to write. What it buys is one invocation at a time per building — the
  atomicity a `GenServer` gives inside one node — plus placement, which is the
  part BEAM cannot do for a building whose node has gone.

  Bodies are the contract's own messages as protobuf JSON, so this stays a
  bridge rather than a second hand-written encoding that could disagree with
  the first.
  """

  require Logger

  alias Pips.Workplace.V1.{
    CanEmployRequest,
    CanEmployResponse,
    DescribeRequest,
    DescribeResponse,
    EndShiftRequest,
    EndShiftResponse,
    ListRequest,
    ListResponse,
    StartShiftRequest,
    StartShiftResponse,
    WorkRequest,
    WorkResponse
  }

  alias Tavern.{Dapr, Workplace}

  defdelegate service_name(), to: Workplace

  def route("List"), do: {:ok, {ListRequest, ListResponse, &list/1}}

  def route("Describe"),
    do: {:ok, {DescribeRequest, DescribeResponse, &forward(&1, "Describe", DescribeResponse)}}

  def route("CanEmploy"),
    do: {:ok, {CanEmployRequest, CanEmployResponse, &forward(&1, "CanEmploy", CanEmployResponse)}}

  def route("StartShift"),
    do:
      {:ok,
       {StartShiftRequest, StartShiftResponse, &forward(&1, "StartShift", StartShiftResponse)}}

  def route("Work"), do: {:ok, {WorkRequest, WorkResponse, &forward(&1, "Work", WorkResponse)}}

  def route("EndShift"),
    do: {:ok, {EndShiftRequest, EndShiftResponse, &forward(&1, "EndShift", EndShiftResponse)}}

  def route(_), do: {:error, :unimplemented}

  @doc """
  Headcount, counted through the actors.

  Asking the local store would read state outside an invocation, and Dapr
  refuses that — so `/healthz` would report a failure on a service that is
  perfectly healthy. There is no shortcut round the sidecar, not even here.
  """
  def workers do
    Enum.reduce(Workplace.buildings_for_actors(), 0, fn b, total ->
      case forward(%DescribeRequest{workplace_id: b.id}, "Describe", DescribeResponse) do
        %DescribeResponse{current_workers: n} -> total + n
        _ -> total
      end
    end)
  end

  # Enumeration is configuration, not state, so it is answered locally. Only the
  # occupancy inside each Describe needs an actor.
  def list(_request) do
    %ListResponse{
      workplaces:
        Enum.map(Workplace.buildings_for_actors(), fn b ->
          forward(%DescribeRequest{workplace_id: b.id}, "Describe", DescribeResponse)
        end)
    }
  end

  def consider_offer(pip, tick) do
    Enum.reduce_while(Workplace.buildings_for_actors(), {false, "no free seats", 0}, fn b, acc ->
      case forward(
             %StartShiftRequest{workplace_id: b.id, pip_id: pip, tick: tick},
             "StartShift",
             StartShiftResponse
           ) do
        %StartShiftResponse{accepted: true} ->
          Logger.info("offer accepted", workplace: b.id, pip: pip, tick: tick)
          {:halt, {true, "", b.id}}

        %StartShiftResponse{reason: reason} ->
          {:cont, put_elem(acc, 1, reason)}

        _ ->
          {:cont, acc}
      end
    end)
  end

  defp forward(request, method, response_module) do
    with {:ok, building} <- building_for(request),
         {:ok, reply} <-
           Dapr.invoke(base(), building, method, Protobuf.JSON.encode!(request)) do
      decode(reply, response_module)
    else
      {:error, :invalid_argument} -> throw({:connect_error, :invalid_argument})
      {:error, :not_found} -> throw({:connect_error, :not_found})
      {:error, reason} -> throw({:connect_error, {:unavailable, reason}})
    end
  end

  # The adapter resolves the id so a request that names no building still
  # reaches one when there is only one, matching the direct handler exactly.
  defp building_for(request) do
    case Workplace.resolve(Map.get(request, :workplace_id, 0)) do
      {:ok, b} -> {:ok, b.id}
      error -> error
    end
  end

  defp decode("", module), do: struct(module)
  defp decode(body, module), do: Protobuf.JSON.decode!(body, module)

  defp base, do: Dapr.sidecar() || raise("Tavern.Workplace.Actor used without a sidecar")
end
