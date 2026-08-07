defmodule Broadcast.WorldService do
  @moduledoc """
  The gRPC client stub for the one RPC this service calls.

  ## Why this is hand-written

  `protoc-gen-elixir` will emit stubs given `plugins=grpc`, but the flag is
  per-invocation and `gen/elixir` is compiled by *every* Elixir service in the
  repo. Turning it on puts `use GRPC.Service` into `bank.pb.ex` and
  `workplace.pb.ex` too, and the tavern — which deliberately has no gRPC
  dependency at all, because it serves Connect with Plug and a hundred lines —
  stops compiling on a stub for a service it will never call.

  So the generated tree stays messages-only, and the single consumer that wants
  a stub declares it. Ten lines here against a dependency in every Elixir
  service is not a close call.

  It also narrows the surface honestly: this service is read-only, and the only
  RPC it is allowed to make is the streaming one below. `JoinWorld`, `Buy` and
  `BuildWorkplace` are absent because a channel handler must never call them —
  see `CLAUDE.md`.

  ## Why gRPC reaches a Connect service at all

  The gateway is built with connect-go, which serves Connect, gRPC and
  gRPC-Web from the same handler. An ordinary gRPC client is therefore a
  supported way in, and the gateway needed no change to be consumed this way.
  """

  defmodule Service do
    @moduledoc false
    use GRPC.Service, name: "pips.world.v1.WorldService"

    rpc(:StreamWorld, Pips.World.V1.StreamWorldRequest, stream(Pips.World.V1.StreamWorldResponse))
  end

  defmodule Stub do
    @moduledoc false
    use GRPC.Stub, service: Service
  end
end
