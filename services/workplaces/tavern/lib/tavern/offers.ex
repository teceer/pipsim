defmodule Tavern.Offers do
  @moduledoc """
  Competing consumer on `pipsim.work.tavern`.

  Several replicas would share the queue and the broker would hand each offer to
  exactly one of them — that is the property RabbitMQ is here for, and the
  reason allocation is not an RPC. Outcomes go back on `pipsim.work.hired`,
  where the gateway turns an acceptance into a Hire intent.

  Rejections are published too, and dropped by the gateway. A full tavern
  declining an offer is the system working; turning that into a retry loop
  would be polling with extra steps.
  """

  use GenServer
  require Logger

  alias Pips.Work.V1.{HireOutcome, WorkOffer}

  @exchange "pipsim.work"
  @queue "pipsim.work.tavern"
  @outcomes "pipsim.work.hired"

  # Matches the farm. A consumer holding more unacked offers than it has seats
  # is just delaying rejections that another replica could have answered.
  @prefetch 4

  def start_link(opts), do: GenServer.start_link(__MODULE__, opts, name: __MODULE__)

  @impl true
  def init(opts) do
    Process.flag(:trap_exit, true)
    {:ok, %{url: Keyword.fetch!(opts, :url), chan: nil}, {:continue, :connect}}
  end

  @impl true
  def handle_continue(:connect, state) do
    case connect(state.url) do
      {:ok, chan} ->
        Logger.info("consuming work offers", queue: @queue, prefetch: @prefetch)
        {:noreply, %{state | chan: chan}}

      {:error, reason} ->
        Logger.warning("could not reach the broker, retrying", reason: inspect(reason))
        Process.send_after(self(), :reconnect, 3_000)
        {:noreply, state}
    end
  end

  @impl true
  def handle_info(:reconnect, state), do: {:noreply, state, {:continue, :connect}}

  # The broker went away. Reconnect rather than crash: the tavern still serves
  # its RPCs without a queue, it simply stops being offered anyone.
  def handle_info({:DOWN, _, :process, _, reason}, state) do
    Logger.warning("broker connection lost", reason: inspect(reason))
    Process.send_after(self(), :reconnect, 3_000)
    {:noreply, %{state | chan: nil}}
  end

  def handle_info({:basic_deliver, payload, %{delivery_tag: tag}}, state) do
    handle_offer(state.chan, payload, tag)
    {:noreply, state}
  end

  def handle_info({:basic_consume_ok, _}, state), do: {:noreply, state}
  def handle_info({:basic_cancel, _}, state), do: {:stop, :normal, state}
  def handle_info({:basic_cancel_ok, _}, state), do: {:noreply, state}
  def handle_info(_other, state), do: {:noreply, state}

  defp connect(url) do
    with {:ok, conn} <- AMQP.Connection.open(url),
         {:ok, chan} <- AMQP.Channel.open(conn),
         :ok <- AMQP.Basic.qos(chan, prefetch_count: @prefetch),
         {:ok, _} <- AMQP.Basic.consume(chan, @queue) do
      Process.monitor(conn.pid)
      {:ok, chan}
    end
  end

  defp handle_offer(chan, payload, tag) do
    offer = Protobuf.decode(payload, WorkOffer)
    {accepted, reason} = Tavern.Workplace.consider_offer(offer.pip_id, offer.tick)

    outcome = %HireOutcome{
      pip_id: offer.pip_id,
      workplace_id: Application.fetch_env!(:tavern, :workplace_id),
      accepted: accepted,
      reason: reason,
      trace_id: offer.trace_id
    }

    AMQP.Basic.publish(chan, @exchange, outcome_key(), Protobuf.encode(outcome))
    AMQP.Basic.ack(chan, tag)
  rescue
    error ->
      # A message we cannot even decode will never succeed on a retry, so it is
      # dropped rather than requeued into a loop.
      Logger.error("undecodable offer, dropping", error: inspect(error))
      AMQP.Basic.reject(chan, tag, requeue: false)
  end

  # The outcomes queue is bound to this key by Terraform layer 20.
  defp outcome_key, do: "hired"

  @doc false
  def queue_name, do: @queue

  @doc false
  def outcomes_queue, do: @outcomes
end
