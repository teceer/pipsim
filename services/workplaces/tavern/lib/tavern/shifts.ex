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

  @ale_per_tick 2

  # What a shift here pays, per tick worked. Alongside need_deltas, not instead
  # of it — ADR 0006 adds money without removing the old channel yet, so wages
  # can be tuned against a living world before need_deltas is retired.
  @wage_per_tick 5

  # How demanding the work is. A scalar the tavern declares about itself;
  # sim-core, not the tavern, decides what effort costs.
  @effort 2

  # What the tavern charges for one unit of ale. Declared here, moved by the
  # bank when a pip buys — the tavern only ever names the number.
  @ale_price 4

  @shift_lease_ms 15_000
  @max_ticks_per_work 40

  defmodule Shift do
    @moduledoc false
    defstruct [:started_tick, :last_work_tick, :last_work_ms]

    @type t :: %__MODULE__{
            started_tick: non_neg_integer(),
            last_work_tick: non_neg_integer(),
            last_work_ms: integer()
          }
  end

  alias Tavern.ShiftSet

  @doc "The knobs the rules need, so ShiftSet does not have to know the tavern."
  def rules,
    do: [
      max_workers: @max_workers,
      lease_ms: @shift_lease_ms,
      max_ticks_per_work: @max_ticks_per_work
    ]

  def max_workers, do: @max_workers
  def shift_lease_ms, do: @shift_lease_ms
  def max_ticks_per_work, do: @max_ticks_per_work
  def wage_per_tick, do: @wage_per_tick
  def effort, do: @effort
  def ale_price, do: @ale_price

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
  @doc """
  What `elapsed` ticks of work here produced and earned.

  No need deltas any more. The tavern used to hand its staff food, which is
  how it once shipped a number that starved them: at -1 a shift here was worse
  than idling, and nobody noticed until the cluster showed tavern staff at 221
  food against the farm's 990. The tavern was never wrong about taverns — it
  was wrong about a number that belongs to the world's metabolism, and it can
  no longer name one. It sells ale and pays wages; what ale does to a pip is
  sim-core's (ADR 0006).
  """
  def effects(elapsed) do
    %{
      produced: @ale_per_tick * elapsed,
      wage: @wage_per_tick * elapsed
    }
  end

  @impl true
  def handle_call({:claim, pip, tick}, _from, state) do
    case ShiftSet.claim(state.shifts, pip, tick, state.clock.(), rules()) do
      {:ok, shifts, expired} ->
        log_expired(expired)
        {:reply, :ok, %{state | shifts: shifts}}

      {:error, reason, shifts, expired} ->
        log_expired(expired)
        {:reply, {:error, reason}, %{state | shifts: shifts}}
    end
  end

  def handle_call({:touch, pip, tick}, _from, state) do
    case ShiftSet.touch(state.shifts, pip, tick, state.clock.(), rules()) do
      :not_on_shift -> {:reply, :not_on_shift, state}
      {:ok, elapsed, shifts} -> {:reply, {:ok, elapsed}, %{state | shifts: shifts}}
    end
  end

  def handle_call({:release, pip}, _from, state) do
    case ShiftSet.release(state.shifts, pip) do
      :not_on_shift -> {:reply, :not_on_shift, state}
      {:ok, started, shifts} -> {:reply, {:ok, started}, %{state | shifts: shifts}}
    end
  end

  def handle_call(:count, _from, state), do: {:reply, map_size(state.shifts), state}

  defp log_expired(pips) do
    Enum.each(pips, fn pip -> Logger.info("shift lease expired", pip: pip) end)
  end
end
