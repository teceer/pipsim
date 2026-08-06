defmodule Pips.World.V1.JoinWorldRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.JoinWorldRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :client_id, 1, type: :string, json_name: "clientId"
end

defmodule Pips.World.V1.JoinWorldResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.JoinWorldResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :tick, 1, type: :uint64
  field :tick_hz, 2, type: :int32, json_name: "tickHz"
  field :sim_seed, 3, type: :uint64, json_name: "simSeed"
  field :pips, 4, repeated: true, type: Pips.Sim.V1.Pip
  field :workplaces, 5, repeated: true, type: Pips.Workplace.V1.DescribeResponse
  field :buildings, 6, repeated: true, type: Pips.Sim.V1.Workplace
end

defmodule Pips.World.V1.StreamWorldRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.StreamWorldRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :client_id, 1, type: :string, json_name: "clientId"
  field :from_tick, 2, type: :uint64, json_name: "fromTick"
end

defmodule Pips.World.V1.StreamWorldResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.StreamWorldResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :delta, 1, type: Pips.Sim.V1.WorldDelta

  field :changed_workplaces, 2,
    repeated: true,
    type: Pips.Workplace.V1.DescribeResponse,
    json_name: "changedWorkplaces"
end

defmodule Pips.World.V1.BuildWorkplaceRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.BuildWorkplaceRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :kind, 1, type: :string
  field :position, 2, type: Pips.Sim.V1.Vec2
end

defmodule Pips.World.V1.BuildWorkplaceResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.BuildWorkplaceResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :ready_at_tick, 2, type: :uint64, json_name: "readyAtTick"
end

defmodule Pips.World.V1.AssignWorkRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.AssignWorkRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :workplace_id, 2, type: :uint64, json_name: "workplaceId"
end

defmodule Pips.World.V1.AssignWorkResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.AssignWorkResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :accepted, 1, type: :bool
  field :reason, 2, type: :string
end

defmodule Pips.World.V1.BuyRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.BuyRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :workplace_id, 2, type: :uint64, json_name: "workplaceId"
  field :kind, 3, type: Pips.Workplace.V1.ResourceKind, enum: true
end

defmodule Pips.World.V1.BuyResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.world.v1.BuyResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :ok, 1, type: :bool
  field :reason, 2, type: :string
  field :price, 3, type: :int64
end
