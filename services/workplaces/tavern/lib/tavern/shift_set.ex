defmodule Tavern.ShiftSet do
  @moduledoc """
  What the tavern *decides*, separated from where it keeps it.

  Reap-then-check-then-take, the lease, and elapsed-tick pricing are one set of
  rules. `Tavern.Shifts` wraps them in a GenServer, where the process is the
  lock; `Tavern.Dapr.Store` loads this, calls one function and writes it back,
  relying on Dapr's actor runtime for the same exclusion.

  Pure on purpose: every function takes the set and the clock reading and
  returns a new set. Nothing here knows what time it is or where the bytes go,
  which is what lets two very different backings share one implementation of
  the rules.

  Note the boundary this does *not* cross. The rules are still written twice in
  the repo — once here and once in Go — and that is deliberate, for the reason
  services/workplaces/CLAUDE.md gives. What is shared is one language's answer
  across two backings, not one answer across two services.
  """

  alias Tavern.Shifts.Shift

  @type t :: %{optional(non_neg_integer()) => Shift.t()}

  @doc """
  Drops shifts whose lease has expired.

  Returns the surviving set and the pips that were dropped, rather than logging
  here: a pure function that writes to the log is only pure until someone reads
  the log.
  """
  @spec reap(t(), integer(), pos_integer()) :: {t(), [non_neg_integer()]}
  def reap(shifts, now_ms, lease_ms) do
    {kept, dropped} =
      Enum.split_with(shifts, fn {_pip, s} -> now_ms - s.last_work_ms <= lease_ms end)

    {Map.new(kept), Enum.map(dropped, fn {pip, _} -> pip end)}
  end

  @doc """
  Reaps, checks capacity and takes a seat.

  One operation, because splitting it into "is there room" and "take the room"
  reintroduces the race whichever backing is underneath.
  """
  @spec claim(t(), non_neg_integer(), non_neg_integer(), integer(), keyword()) ::
          {:ok, t(), [non_neg_integer()]} | {:error, String.t(), t(), [non_neg_integer()]}
  def claim(shifts, pip, tick, now_ms, opts) do
    {shifts, expired} = reap(shifts, now_ms, Keyword.fetch!(opts, :lease_ms))

    cond do
      Map.has_key?(shifts, pip) ->
        {:error, "already on shift here", shifts, expired}

      map_size(shifts) >= Keyword.fetch!(opts, :max_workers) ->
        {:error, "no free seats", shifts, expired}

      true ->
        shift = %Shift{started_tick: tick, last_work_tick: tick, last_work_ms: now_ms}
        {:ok, Map.put(shifts, pip, shift), expired}
    end
  end

  @doc "Renews the lease and reports how many ticks the shift is owed for."
  @spec touch(t(), non_neg_integer(), non_neg_integer(), integer(), keyword()) ::
          {:ok, non_neg_integer(), t()} | :not_on_shift
  def touch(shifts, pip, tick, now_ms, opts) do
    case shifts[pip] do
      nil ->
        :not_on_shift

      %Shift{} = shift ->
        elapsed =
          min(max(tick - shift.last_work_tick, 0), Keyword.fetch!(opts, :max_ticks_per_work))

        updated = %Shift{shift | last_work_tick: tick, last_work_ms: now_ms}
        {:ok, elapsed, Map.put(shifts, pip, updated)}
    end
  end

  @spec release(t(), non_neg_integer()) ::
          {:ok, non_neg_integer(), t()} | :not_on_shift
  def release(shifts, pip) do
    case Map.pop(shifts, pip) do
      {nil, _} -> :not_on_shift
      {shift, rest} -> {:ok, shift.started_tick, rest}
    end
  end
end
