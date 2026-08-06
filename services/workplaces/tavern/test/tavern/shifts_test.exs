defmodule Tavern.ShiftsTest do
  @moduledoc """
  The same properties the farm's tests pin, checked against a second
  implementation in a second language.

  That is the point of these rather than a copy of them: the workplace contract
  claims a building type can be added in any language without touching the core
  or the gateway. Until now that was an assertion in a README. These tests are
  what make it a checked one.
  """

  use ExUnit.Case, async: true

  alias Tavern.Shifts

  setup do
    # A clock the test owns, so fifteen seconds cost nothing.
    {:ok, clock} = Agent.start_link(fn -> 0 end)
    now = fn -> Agent.get(clock, & &1) end
    advance = fn ms -> Agent.update(clock, &(&1 + ms)) end

    name = :"shifts_#{System.unique_integer([:positive])}"
    {:ok, _} = Shifts.start_link(name: name, clock: now)

    %{shifts: name, advance: advance}
  end

  test "capacity is enforced", %{shifts: s} do
    for pip <- 1..Shifts.max_workers() do
      assert :ok = Shifts.claim(s, pip, 1)
    end

    assert {:error, "no free seats"} = Shifts.claim(s, 9999, 1)
    assert Shifts.count(s) == Shifts.max_workers()
  end

  test "the same pip cannot take two seats", %{shifts: s} do
    assert :ok = Shifts.claim(s, 7, 1)
    assert {:error, "already on shift here"} = Shifts.claim(s, 7, 1)
  end

  # The failure this reproduces was seen in the cluster against the farm:
  # sim-core restarted with a fresh world and the workplace went on holding
  # every position for pips that no longer existed, so nobody could be hired.
  test "abandoned shifts expire and free their seats", %{shifts: s, advance: advance} do
    for pip <- 1..Shifts.max_workers(), do: Shifts.claim(s, pip, 1)
    assert {:error, _} = Shifts.claim(s, 9999, 1)

    # Nobody works these shifts; time simply passes.
    advance.(Shifts.shift_lease_ms() + 1_000)

    assert :ok = Shifts.claim(s, 9999, 1)
    assert Shifts.count(s) == 1, "the ghosts should have been reaped"
  end

  test "working renews the lease", %{shifts: s, advance: advance} do
    assert :ok = Shifts.claim(s, 7, 0)

    for tick <- 1..5 do
      advance.(Shifts.shift_lease_ms() - 1_000)
      assert {:ok, _} = Shifts.touch(s, 7, tick)
    end

    assert Shifts.count(s) == 1
  end

  test "work for an unknown pip reports no shift", %{shifts: s} do
    assert :not_on_shift = Shifts.touch(s, 404, 1)
  end

  # The bug this pins cost the farm a debugging session: the gateway batches,
  # calling Work once a second while the world ticks ten times. Flat per-call
  # amounts make the contract depend on the caller's cadence.
  test "work pays for every elapsed tick", %{shifts: s} do
    assert :ok = Shifts.claim(s, 3, 1)
    assert {:ok, 10} = Shifts.touch(s, 3, 11)

    %{produced: ale, wage: wage} = Shifts.effects(10)
    assert ale == 20
    assert wage == 10 * Shifts.wage_per_tick()
  end

  test "work twice in one tick pays once", %{shifts: s} do
    assert :ok = Shifts.claim(s, 4, 5)
    assert {:ok, 0} = Shifts.touch(s, 4, 5)
  end

  test "a long gap is capped", %{shifts: s} do
    assert :ok = Shifts.claim(s, 5, 1)
    assert {:ok, elapsed} = Shifts.touch(s, 5, 100_000)
    assert elapsed == Shifts.max_ticks_per_work()
  end

  test "releasing frees the seat and reports the shift length", %{shifts: s} do
    assert :ok = Shifts.claim(s, 8, 100)
    assert {:ok, 100} = Shifts.release(s, 8)
    assert Shifts.count(s) == 0
    assert :not_on_shift = Shifts.release(s, 8)
  end

  # The tavern can no longer say anything about a pip's needs, and that is the
  # point of ADR 0006. It once shipped food_per_tick = -1 and starved everyone
  # it employed; the fix is not a better number but a contract in which the
  # number cannot be named here at all. What ale does to a pip is sim-core's.
  test "a shift reports what it produced and what it paid, and nothing else",
       %{shifts: _s} do
    effects = Shifts.effects(1)
    assert Map.keys(effects) |> Enum.sort() == [:produced, :wage]
    assert effects.wage > 0
  end
end
