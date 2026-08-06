defmodule Tavern.Shifts do
  @moduledoc """
  Who is on shift, and the rules the tavern owns.

  Everything the farm learned the hard way applies here, and is deliberately
  reimplemented rather than shared: a workplace is a service, and two services
  sharing a library is how "no domain rule in two languages" quietly becomes
  "one rule, two deployments, one of them stale". What is shared is the
  contract, not the implementation.

  Three of those lessons are load-bearing:

  * **Shifts are held on a lease.** One nobody asks to `Work` for fifteen
    seconds expires by itself, so the tavern never has to ask anyone who still
    exists. Without it, a sim-core restart leaves every position held by a pip
    that is gone.
  * **`Work` pays for elapsed ticks, not per call.** The gateway batches — one
    call a second against ten ticks — so flat per-call amounts would make the
    contract depend on the caller's cadence.
  * **Capacity, check and claim are one operation.** This is a GenServer, so
    that is free: the process *is* the lock.

  State is in this process, which is correct at one replica and only at one.
  The farm hit this at two and moved its shifts to Redis; the same move applies
  here the day the tavern scales, and `replicaCount` in the chart says so.
  """

  use GenServer
  require Logger

  # Smaller than the farm. Scarcity is the point: a tavern that seats everyone
  # would make the allocation problem disappear, and with it the reason two
  # workplaces compete for the same pips.
  @max_workers 8

  # Keyed by pips.sim.v1.Need.
  @need_food 1
  @need_rest 2
  @need_social 3

  @ale_per_tick 2

  # What a shift here does to the worker, per tick.
  #
  # This is the only workplace that gives more than it takes, and the only one
  # that touches `social` at all — until now that need decayed toward nothing
  # with nothing in the world able to restore it. Pulling ale is sociable work;
  # it is also tiring, and it does not feed you.
  @social_per_tick 6
  @rest_per_tick 2
  @food_per_tick -1

  @shift_lease_ms 15_000
  @max_ticks_per_work 40

  defmodule Shift do
    @moduledoc false
    defstruct [:started_tick, :last_work_tick, :last_work_ms]
  end

  def max_workers, do: @max_workers
  def shift_lease_ms, do: @shift_lease_ms
  def max_ticks_per_work, do: @max_ticks_per_work

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: opts[:name] || __MODULE__)
  end

  @impl true
  def init(opts) do
    {:ok,
     %{
       shifts: %{},
       # Injectable so tests can make fifteen seconds pass for free.
       clock: Keyword.get(opts, :clock, fn -> System.monotonic_time(:millisecond) end)
     }}
  end

  @doc "Atomically reap, check capacity and take a position."
  def claim(server \\ __MODULE__, pip, tick), do: GenServer.call(server, {:claim, pip, tick})

  @doc "Renew the lease and report how many ticks the shift is owed for."
  def touch(server \\ __MODULE__, pip, tick), do: GenServer.call(server, {:touch, pip, tick})

  def release(server \\ __MODULE__, pip), do: GenServer.call(server, {:release, pip})

  def count(server \\ __MODULE__), do: GenServer.call(server, :count)

  @doc "What a tick of work here does, given how many ticks elapsed."
  def effects(elapsed) do
    %{
      produced: @ale_per_tick * elapsed,
      needs: %{
        @need_social => @social_per_tick * elapsed,
        @need_rest => @rest_per_tick * elapsed,
        @need_food => @food_per_tick * elapsed
      }
    }
  end

  @impl true
  def handle_call({:claim, pip, tick}, _from, state) do
    state = reap(state)

    cond do
      Map.has_key?(state.shifts, pip) ->
        {:reply, {:error, "already on shift here"}, state}

      map_size(state.shifts) >= @max_workers ->
        {:reply, {:error, "no free seats"}, state}

      true ->
        shift = %Shift{started_tick: tick, last_work_tick: tick, last_work_ms: state.clock.()}
        {:reply, :ok, put_in(state.shifts[pip], shift)}
    end
  end

  def handle_call({:touch, pip, tick}, _from, state) do
    case state.shifts[pip] do
      nil ->
        {:reply, :not_on_shift, state}

      %Shift{} = shift ->
        elapsed = min(max(tick - shift.last_work_tick, 0), @max_ticks_per_work)

        updated = %Shift{
          shift
          | last_work_tick: tick,
            last_work_ms: state.clock.()
        }

        {:reply, {:ok, elapsed}, put_in(state.shifts[pip], updated)}
    end
  end

  def handle_call({:release, pip}, _from, state) do
    case Map.pop(state.shifts, pip) do
      {nil, _} -> {:reply, :not_on_shift, state}
      {shift, rest} -> {:reply, {:ok, shift.started_tick}, %{state | shifts: rest}}
    end
  end

  def handle_call(:count, _from, state), do: {:reply, map_size(state.shifts), state}

  defp reap(state) do
    now = state.clock.()

    {kept, dropped} =
      Enum.split_with(state.shifts, fn {_pip, s} -> now - s.last_work_ms <= @shift_lease_ms end)

    Enum.each(dropped, fn {pip, _} -> Logger.info("shift lease expired", pip: pip) end)
    %{state | shifts: Map.new(kept)}
  end
end
