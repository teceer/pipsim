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
  @dlx "pipsim.work.dlx"
  @queue "pipsim.work.tavern"
  @outcomes "pipsim.work.hired"
  @offer_key "offer.tavern"

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
         :ok <- declare(chan),
         :ok <- AMQP.Basic.qos(chan, prefetch_count: @prefetch),
         {:ok, _} <- AMQP.Basic.consume(chan, @queue) do
      Process.monitor(conn.pid)
      {:ok, chan}
    end
  end

  # The tavern owns the queue it consumes, and declares it on every connection.
  #
  # Before this, the topology existed only in Terraform layer 20, so the tavern
  # could not start against a broker nobody had provisioned — `make dev` brings
  # up a bare RabbitMQ and the consume failed with NOT_FOUND, which took the
  # whole application down through the supervisor. That contradicted the rule
  # that every service runs without a cluster.
  #
  # AMQP declarations are idempotent for identical arguments and a channel error
  # otherwise, so this is a no-op against a broker layer 20 already configured —
  # provided the settings below stay in step with it.
  defp declare(chan) do
    with :ok <- AMQP.Exchange.declare(chan, @exchange, :topic, durable: true),
         # The queue names this as its dead-letter target, and a DLX that does
         # not exist means dead letters are dropped in silence.
         :ok <- AMQP.Exchange.declare(chan, @dlx, :fanout, durable: true),
         {:ok, _} <-
           AMQP.Queue.declare(chan, @queue,
             durable: true,
             arguments: [{"x-dead-letter-exchange", :longstr, @dlx}]
           ) do
      AMQP.Queue.bind(chan, @queue, @exchange, routing_key: @offer_key)
    end
  end

  defp handle_offer(chan, payload, tag) do
    offer = Protobuf.decode(payload, WorkOffer)
    {accepted, reason, workplace_id} = Tavern.Workplace.consider_offer(offer.pip_id, offer.tick)

    # The id comes from whichever building claimed the offer, not from
    # configuration. `Application.fetch_env!(:tavern, :workplace_id)` stood here
    # and no longer resolved to anything: a tavern process hosts many buildings
    # since ADR 0005, so that key was dropped. It raised on every accepted
    # offer, the rescue below rejected the message, and no pip was ever hired
    # through this queue — invisible until now, because the service could not
    # start against a broker without the queue in the first place.
    outcome = %HireOutcome{
      pip_id: offer.pip_id,
      workplace_id: workplace_id,
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

  # The gateway consumes the outcomes queue, so the gateway is what binds it to
  # this key. Publishing to a topic exchange with no matching binding is not an
  # error — if no gateway is running, the outcome is dropped, which is what
  # should happen to an answer nobody is waiting for.
  defp outcome_key, do: "hired"

  @doc false
  def queue_name, do: @queue

  @doc false
  def outcomes_queue, do: @outcomes
end
