defmodule Tavern.Workplace do
  @moduledoc """
  pips.workplace.v1.WorkplaceService, and nothing else.

  Six RPCs now — `List` joined the five, because a workplace service owns a
  *kind* of building and may host several, so "who are you" stopped having an
  answer. See ADR 0005. If this module grows a seventh, the abstraction has
  leaked; see services/workplaces/CLAUDE.md.

  Where shifts are kept is a `Tavern.Store`. This module never touches state
  directly, which is what lets the same six functions run against a `GenServer`
  per building or against the Dapr actor state store without knowing which.
  """

  require Logger

  alias Pips.Sim.V1.Vec2

  alias Pips.Workplace.V1.{
    BuyRequest,
    BuyResponse,
    CanEmployRequest,
    CanEmployResponse,
    DescribeRequest,
    DescribeResponse,
    EndShiftRequest,
    EndShiftResponse,
    ListRequest,
    ListResponse,
    Offer,
    ResourceAmount,
    StartShiftRequest,
    StartShiftResponse,
    WorkRequest,
    WorkResponse
  }

  alias Tavern.Shifts

  def service_name, do: "pips.workplace.v1.WorkplaceService"

  def route("List"), do: {:ok, {ListRequest, ListResponse, &list/1}}
  def route("Describe"), do: {:ok, {DescribeRequest, DescribeResponse, &describe/1}}
  def route("CanEmploy"), do: {:ok, {CanEmployRequest, CanEmployResponse, &can_employ/1}}
  def route("StartShift"), do: {:ok, {StartShiftRequest, StartShiftResponse, &start_shift/1}}
  def route("Work"), do: {:ok, {WorkRequest, WorkResponse, &work/1}}
  def route("EndShift"), do: {:ok, {EndShiftRequest, EndShiftResponse, &end_shift/1}}
  def route("Buy"), do: {:ok, {BuyRequest, BuyResponse, &buy/1}}
  def route(_), do: {:error, :unimplemented}

  @doc "Headcount across every building, for /healthz."
  def workers do
    Enum.reduce(buildings(), 0, fn b, total -> total + store().count(b.id) end)
  end

  def list(_request) do
    %ListResponse{workplaces: Enum.map(buildings(), &describe_building/1)}
  end

  def describe(%DescribeRequest{workplace_id: id}) do
    describe_building(resolve!(id))
  end

  # Advisory, and always has been: it reports headroom, and the seat is only
  # actually taken by StartShift or by accepting an offer.
  def can_employ(%CanEmployRequest{workplace_id: id}) do
    b = resolve!(id)

    if store().count(b.id) >= Shifts.max_workers() do
      %CanEmployResponse{allowed: false, reason: "no free seats"}
    else
      %CanEmployResponse{allowed: true}
    end
  end

  def start_shift(%StartShiftRequest{workplace_id: id, pip_id: pip, tick: tick}) do
    b = resolve!(id)

    case store().claim(b.id, pip, tick) do
      :ok ->
        Logger.info("shift started", workplace: b.id, pip: pip, tick: tick)
        %StartShiftResponse{accepted: true}

      {:error, reason} ->
        %StartShiftResponse{accepted: false, reason: reason}
    end
  end

  def work(%WorkRequest{workplace_id: id, pip_id: pip, tick: tick}) do
    b = resolve!(id)

    case store().touch(b.id, pip, tick) do
      # Not an error: sim-core may have decided the pip is dead or has left, and
      # the caller finding out here is the normal way that surfaces.
      :not_on_shift ->
        %WorkResponse{shift_should_end: true}

      # Called twice within the same tick. Paying again would let a chatty
      # caller mint ale.
      {:ok, 0} ->
        %WorkResponse{}

      {:ok, elapsed} ->
        %{produced: ale, wage: wage} = Shifts.effects(elapsed)

        %WorkResponse{
          produced: [%ResourceAmount{kind: :RESOURCE_KIND_ALE, amount: ale}],
          wage: wage
        }
    end
  end

  @doc """
  A pip buys one unit of what the tavern sells.

  Only ever confirms the kind and the price `describe/1` already advertised —
  the tavern never moves money itself. The gateway does that through the bank,
  then tells sim-core what ale does to the pip via a TransferIntent.
  """
  def buy(%BuyRequest{kind: :RESOURCE_KIND_ALE}) do
    %BuyResponse{
      ok: true,
      price: Shifts.ale_price(),
      produced: %ResourceAmount{kind: :RESOURCE_KIND_ALE, amount: 1}
    }
  end

  def buy(%BuyRequest{}) do
    %BuyResponse{ok: false, reason: "the tavern sells only ale"}
  end

  def end_shift(%EndShiftRequest{workplace_id: id, pip_id: pip, tick: tick, reason: reason}) do
    b = resolve!(id)

    case store().release(b.id, pip) do
      {:ok, started} ->
        Logger.info("shift ended",
          workplace: b.id,
          pip: pip,
          reason: reason,
          ticks_worked: tick - started
        )

      :not_on_shift ->
        :ok
    end

    %EndShiftResponse{}
  end

  @doc """
  Answers a work offer taken off the queue.

  The queue is keyed by kind rather than by building, so an offer arriving here
  is not addressed to any particular tavern — this picks one. Buildings are
  tried in order and the first to claim wins; with one building, which is the
  common case, that is exactly what it was before.
  """
  def consider_offer(pip, tick) do
    Enum.reduce_while(buildings(), {false, "no free seats"}, fn b, acc ->
      case store().claim(b.id, pip, tick) do
        :ok ->
          Logger.info("offer accepted", workplace: b.id, pip: pip, tick: tick)
          {:halt, {true, ""}}

        {:error, reason} ->
          {:cont, put_elem(acc, 1, reason)}
      end
    end)
  end

  @doc """
  The building an RPC names.

  A zero id is accepted only when this process hosts exactly one building. Not
  politeness towards old callers: it is what lets a single-building service keep
  answering `Describe{}` the way the conformance suite and a pre-List gateway
  expect, while a multi-building one refuses to guess.
  """
  def resolve(id) do
    case {id, buildings()} do
      {0, [only]} ->
        {:ok, only}

      {0, _many} ->
        {:error, :invalid_argument}

      {id, all} ->
        Enum.find(all, &(&1.id == id)) |> then(&if &1, do: {:ok, &1}, else: {:error, :not_found})
    end
  end

  defp resolve!(id) do
    case resolve(id) do
      {:ok, b} -> b
      {:error, reason} -> throw({:connect_error, reason})
    end
  end

  defp describe_building(b) do
    %DescribeResponse{
      workplace_id: b.id,
      kind: "tavern",
      # Carries the id: several taverns now share a process, and "Tavern" three
      # times over is unreadable in a log line or on the map.
      display_name: "Tavern ##{b.id}",
      max_workers: Shifts.max_workers(),
      current_workers: store().count(b.id),
      position: %Vec2{x_milli: b.x, y_milli: b.y},
      produces: [:RESOURCE_KIND_ALE],
      consumes: [:RESOURCE_KIND_GRAIN],
      wage: Shifts.wage_per_tick(),
      effort: Shifts.effort(),
      sells: [%Offer{kind: :RESOURCE_KIND_ALE, price: Shifts.ale_price()}]
    }
  end

  @doc "The hosted buildings. Public so the actor adapter can enumerate them."
  def buildings_for_actors, do: buildings()

  defp buildings, do: Application.fetch_env!(:tavern, :buildings)
  defp store, do: Application.get_env(:tavern, :store, Tavern.Store.Process)
end
