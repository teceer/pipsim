defmodule Pips.Workplace.V1.ResourceKind do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "pips.workplace.v1.ResourceKind",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :RESOURCE_KIND_UNSPECIFIED, 0
  field :RESOURCE_KIND_GRAIN, 1
  field :RESOURCE_KIND_FOOD, 2
  field :RESOURCE_KIND_TOOL, 3
  field :RESOURCE_KIND_ALE, 4
end

defmodule Pips.Workplace.V1.DescribeRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.DescribeRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
end

defmodule Pips.Workplace.V1.DescribeResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.DescribeResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :kind, 2, type: :string
  field :display_name, 3, type: :string, json_name: "displayName"
  field :max_workers, 4, type: :int32, json_name: "maxWorkers"
  field :current_workers, 5, type: :int32, json_name: "currentWorkers"
  field :position, 6, type: Pips.Sim.V1.Vec2
  field :produces, 7, repeated: true, type: Pips.Workplace.V1.ResourceKind, enum: true
  field :consumes, 8, repeated: true, type: Pips.Workplace.V1.ResourceKind, enum: true
end

defmodule Pips.Workplace.V1.CanEmployRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.CanEmployRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :pip_id, 2, type: :uint64, json_name: "pipId"
end

defmodule Pips.Workplace.V1.CanEmployResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.CanEmployResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :allowed, 1, type: :bool
  field :reason, 2, type: :string
end

defmodule Pips.Workplace.V1.StartShiftRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.StartShiftRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :pip_id, 2, type: :uint64, json_name: "pipId"
  field :tick, 3, type: :uint64
end

defmodule Pips.Workplace.V1.StartShiftResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.StartShiftResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :accepted, 1, type: :bool
  field :reason, 2, type: :string
end

defmodule Pips.Workplace.V1.WorkRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.WorkRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :pip_id, 2, type: :uint64, json_name: "pipId"
  field :tick, 3, type: :uint64
end

defmodule Pips.Workplace.V1.WorkResponse.NeedDeltasEntry do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.WorkResponse.NeedDeltasEntry",
    map: true,
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :key, 1, type: :int32
  field :value, 2, type: :int32
end

defmodule Pips.Workplace.V1.WorkResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.WorkResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :produced, 1, repeated: true, type: Pips.Workplace.V1.ResourceAmount

  field :need_deltas, 2,
    repeated: true,
    type: Pips.Workplace.V1.WorkResponse.NeedDeltasEntry,
    json_name: "needDeltas",
    map: true

  field :shift_should_end, 3, type: :bool, json_name: "shiftShouldEnd"
end

defmodule Pips.Workplace.V1.ResourceAmount do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.ResourceAmount",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :kind, 1, type: Pips.Workplace.V1.ResourceKind, enum: true
  field :amount, 2, type: :int32
end

defmodule Pips.Workplace.V1.EndShiftRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.EndShiftRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :pip_id, 2, type: :uint64, json_name: "pipId"
  field :tick, 3, type: :uint64
  field :reason, 4, type: :string
end

defmodule Pips.Workplace.V1.EndShiftResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.workplace.v1.EndShiftResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3
end
