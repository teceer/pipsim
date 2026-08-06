defmodule Tavern.Store do
  @moduledoc """
  Where a building's shifts are kept, as opposed to what the rules are.

  The rules live once, in `Tavern.ShiftSet`. This says who serialises access to
  them, and there are two answers:

  * `Tavern.Store.Process` — a `GenServer` per building. The process *is* the
    lock, so reap-check-claim is atomic for free. Correct at one replica.
  * `Tavern.Store.Dapr` — the actor state store behind the sidecar. Dapr allows
    one invocation at a time per building, so a plain read-modify-write is
    already indivisible. Correct at any number.

  Worth naming the awkwardness rather than hiding it: the second is the first,
  bought from somewhere else. A `GenServer` per building under a supervisor is
  the actor model, and BEAM has had it since before Kubernetes existed. What
  Dapr adds is placement — surviving the process that holds the building — and
  that is the only reason to pay for the round trip. See ADR 0005.
  """

  @type building :: non_neg_integer()

  @callback claim(building(), non_neg_integer(), non_neg_integer()) ::
              :ok | {:error, String.t()}
  @callback touch(building(), non_neg_integer(), non_neg_integer()) ::
              {:ok, non_neg_integer()} | :not_on_shift
  @callback release(building(), non_neg_integer()) ::
              {:ok, non_neg_integer()} | :not_on_shift
  @callback count(building()) :: non_neg_integer()
end

defmodule Tavern.Store.Process do
  @moduledoc """
  A `GenServer` per building, addressed through a `Registry`.

  This is the idiomatic answer in this language and it is genuinely good: two
  taverns in one process share nothing, and one crashing does not disturb the
  other. What it cannot do is survive the node — which is what the Dapr store
  is for.
  """

  @behaviour Tavern.Store

  alias Tavern.Shifts

  def registry, do: Tavern.Shifts.Registry

  @doc "The name a building's shift process answers to."
  def via(building), do: {:via, Registry, {registry(), building}}

  @impl true
  def claim(building, pip, tick), do: Shifts.claim(via(building), pip, tick)

  @impl true
  def touch(building, pip, tick), do: Shifts.touch(via(building), pip, tick)

  @impl true
  def release(building, pip), do: Shifts.release(via(building), pip)

  @impl true
  def count(building), do: Shifts.count(via(building))
end
