defmodule Tavern.Workplace do
  @moduledoc """
  pips.workplace.v1.WorkplaceService, and nothing else.

  Five RPCs, no knowledge that any other workplace exists, and no pip state
  beyond who is on shift right now. If this module ever grows a sixth method,
  the abstraction has leaked — see services/workplaces/CLAUDE.md.
  """

  require Logger

  alias Pips.Sim.V1.Vec2

  alias Pips.Workplace.V1.{
    CanEmployRequest,
    CanEmployResponse,
    DescribeRequest,
    DescribeResponse,
    EndShiftRequest,
    EndShiftResponse,
    ResourceAmount,
    StartShiftRequest,
    StartShiftResponse,
    WorkRequest,
    WorkResponse
  }

  alias Tavern.Shifts

  def service_name, do: "pips.workplace.v1.WorkplaceService"

  def route("Describe"), do: {:ok, {DescribeRequest, DescribeResponse, &describe/1}}
  def route("CanEmploy"), do: {:ok, {CanEmployRequest, CanEmployResponse, &can_employ/1}}
  def route("StartShift"), do: {:ok, {StartShiftRequest, StartShiftResponse, &start_shift/1}}
  def route("Work"), do: {:ok, {WorkRequest, WorkResponse, &work/1}}
  def route("EndShift"), do: {:ok, {EndShiftRequest, EndShiftResponse, &end_shift/1}}
  def route(_), do: {:error, :unimplemented}

  def workers, do: Shifts.count()

  def describe(_request) do
    %DescribeResponse{
      workplace_id: config(:workplace_id),
      kind: "tavern",
      display_name: "Tavern",
      max_workers: Shifts.max_workers(),
      current_workers: Shifts.count(),
      position: %Vec2{x_milli: config(:x), y_milli: config(:y)},
      produces: [:RESOURCE_KIND_ALE],
      consumes: [:RESOURCE_KIND_GRAIN]
    }
  end

  # Advisory, and always has been: it reports headroom, and the seat is only
  # actually taken by StartShift or by accepting an offer.
  def can_employ(_request) do
    if Shifts.count() >= Shifts.max_workers() do
      %CanEmployResponse{allowed: false, reason: "no free seats"}
    else
      %CanEmployResponse{allowed: true}
    end
  end

  def start_shift(%StartShiftRequest{pip_id: pip, tick: tick}) do
    case Shifts.claim(pip, tick) do
      :ok ->
        Logger.info("shift started", pip: pip, tick: tick)
        %StartShiftResponse{accepted: true}

      {:error, reason} ->
        %StartShiftResponse{accepted: false, reason: reason}
    end
  end

  def work(%WorkRequest{pip_id: pip, tick: tick}) do
    case Shifts.touch(pip, tick) do
      # Not an error: sim-core may have decided the pip is dead or has left, and
      # the caller finding out here is the normal way that surfaces.
      :not_on_shift ->
        %WorkResponse{shift_should_end: true}

      # Called twice within the same tick. Paying again would let a chatty
      # caller mint ale.
      {:ok, 0} ->
        %WorkResponse{}

      {:ok, elapsed} ->
        %{produced: ale, needs: needs} = Shifts.effects(elapsed)

        %WorkResponse{
          produced: [%ResourceAmount{kind: :RESOURCE_KIND_ALE, amount: ale}],
          need_deltas: needs
        }
    end
  end

  def end_shift(%EndShiftRequest{pip_id: pip, tick: tick, reason: reason}) do
    case Shifts.release(pip) do
      {:ok, started} ->
        Logger.info("shift ended", pip: pip, reason: reason, ticks_worked: tick - started)

      :not_on_shift ->
        :ok
    end

    %EndShiftResponse{}
  end

  @doc """
  Answers a work offer taken off the queue.

  Capacity check and claim are one operation because the GenServer serialises
  them — no window for a second consumer to take the last seat in between.
  """
  def consider_offer(pip, tick) do
    case Shifts.claim(pip, tick) do
      :ok ->
        Logger.info("offer accepted", pip: pip, tick: tick)
        {true, ""}

      {:error, reason} ->
        {false, reason}
    end
  end

  defp config(key), do: Application.fetch_env!(:tavern, key)
end
