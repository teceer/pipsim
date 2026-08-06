defmodule Pips.Sim.V1.Need do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "pips.sim.v1.Need",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :NEED_UNSPECIFIED, 0
  field :NEED_FOOD, 1
  field :NEED_REST, 2
  field :NEED_SOCIAL, 3
end

defmodule Pips.Sim.V1.PipActivity do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "pips.sim.v1.PipActivity",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :PIP_ACTIVITY_UNSPECIFIED, 0
  field :PIP_ACTIVITY_IDLE, 1
  field :PIP_ACTIVITY_WALKING, 2
  field :PIP_ACTIVITY_WORKING, 3
  field :PIP_ACTIVITY_EATING, 4
  field :PIP_ACTIVITY_SLEEPING, 5
  field :PIP_ACTIVITY_COMMUTING, 6
end

defmodule Pips.Sim.V1.Vec2 do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.Vec2",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :x_milli, 1, type: :int32, json_name: "xMilli"
  field :y_milli, 2, type: :int32, json_name: "yMilli"
end

defmodule Pips.Sim.V1.Pip.NeedsEntry do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.Pip.NeedsEntry",
    map: true,
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :key, 1, type: :int32
  field :value, 2, type: :int32
end

defmodule Pips.Sim.V1.Pip do
  @moduledoc false

  use Protobuf, full_name: "pips.sim.v1.Pip", protoc_gen_elixir_version: "0.17.0", syntax: :proto3

  field :id, 1, type: :uint64
  field :name, 2, type: :string
  field :position, 3, type: Pips.Sim.V1.Vec2
  field :activity, 4, type: Pips.Sim.V1.PipActivity, enum: true
  field :needs, 5, repeated: true, type: Pips.Sim.V1.Pip.NeedsEntry, map: true

  field :employer_workplace_id, 6,
    proto3_optional: true,
    type: :uint64,
    json_name: "employerWorkplaceId"

  field :inside_workplace_id, 7,
    proto3_optional: true,
    type: :uint64,
    json_name: "insideWorkplaceId"
end

defmodule Pips.Sim.V1.Workplace do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.Workplace",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :id, 1, type: :uint64
  field :kind, 2, type: :string
  field :position, 3, type: Pips.Sim.V1.Vec2
  field :capacity, 4, type: :uint32
  field :occupants, 5, type: :uint32
end

defmodule Pips.Sim.V1.StepRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.StepRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :tick, 1, type: :uint64
end

defmodule Pips.Sim.V1.StepResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.StepResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :tick, 1, type: :uint64
  field :delta, 2, type: Pips.Sim.V1.WorldDelta
  field :domain_events, 3, repeated: true, type: :bytes, json_name: "domainEvents"
end

defmodule Pips.Sim.V1.WorldDelta do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.WorldDelta",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :tick, 1, type: :uint64
  field :pips, 2, repeated: true, type: Pips.Sim.V1.PipDelta
  field :removed_pip_ids, 3, repeated: true, type: :uint64, json_name: "removedPipIds"
  field :workplaces, 4, repeated: true, type: Pips.Sim.V1.Workplace
end

defmodule Pips.Sim.V1.PipDelta.NeedsEntry do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.PipDelta.NeedsEntry",
    map: true,
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :key, 1, type: :int32
  field :value, 2, type: :int32
end

defmodule Pips.Sim.V1.PipDelta do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.PipDelta",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :id, 1, type: :uint64
  field :position, 2, proto3_optional: true, type: Pips.Sim.V1.Vec2
  field :activity, 3, proto3_optional: true, type: Pips.Sim.V1.PipActivity, enum: true
  field :needs, 4, repeated: true, type: Pips.Sim.V1.PipDelta.NeedsEntry, map: true

  field :inside_workplace_id, 5,
    proto3_optional: true,
    type: :uint64,
    json_name: "insideWorkplaceId"
end

defmodule Pips.Sim.V1.SnapshotRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.SnapshotRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3
end

defmodule Pips.Sim.V1.SnapshotResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.SnapshotResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :tick, 1, type: :uint64
  field :pips, 2, repeated: true, type: Pips.Sim.V1.Pip
  field :state_hash, 3, type: :bytes, json_name: "stateHash"
  field :workplaces, 4, repeated: true, type: Pips.Sim.V1.Workplace
end

defmodule Pips.Sim.V1.WatchDeltasRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.WatchDeltasRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :from_tick, 1, type: :uint64, json_name: "fromTick"
end

defmodule Pips.Sim.V1.WatchDeltasResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.WatchDeltasResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :delta, 1, type: Pips.Sim.V1.WorldDelta
end

defmodule Pips.Sim.V1.SubmitIntentRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.SubmitIntentRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  oneof :intent, 0

  field :hire, 1, type: Pips.Sim.V1.HireIntent, oneof: 0
  field :move, 2, type: Pips.Sim.V1.MoveIntent, oneof: 0
  field :spawn, 3, type: Pips.Sim.V1.SpawnPipIntent, oneof: 0
  field :apply_needs, 4, type: Pips.Sim.V1.ApplyNeedsIntent, json_name: "applyNeeds", oneof: 0

  field :register_workplace, 5,
    type: Pips.Sim.V1.RegisterWorkplaceIntent,
    json_name: "registerWorkplace",
    oneof: 0

  field :end_employment, 6,
    type: Pips.Sim.V1.EndEmploymentIntent,
    json_name: "endEmployment",
    oneof: 0

  field :transfer, 7, type: Pips.Sim.V1.TransferIntent, oneof: 0

  field :credit_balances, 8,
    type: Pips.Sim.V1.CreditBalancesIntent,
    json_name: "creditBalances",
    oneof: 0
end

defmodule Pips.Sim.V1.SubmitIntentResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.SubmitIntentResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :accepted, 1, type: :bool
  field :rejection_reason, 2, type: :string, json_name: "rejectionReason"
  field :scheduled_tick, 3, type: :uint64, json_name: "scheduledTick"
end

defmodule Pips.Sim.V1.HireIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.HireIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :workplace_id, 2, type: :uint64, json_name: "workplaceId"
end

defmodule Pips.Sim.V1.EndEmploymentIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.EndEmploymentIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
end

defmodule Pips.Sim.V1.RegisterWorkplaceIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.RegisterWorkplaceIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :kind, 2, type: :string
  field :position, 3, type: Pips.Sim.V1.Vec2
  field :capacity, 4, type: :uint32
end

defmodule Pips.Sim.V1.MoveIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.MoveIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :destination, 2, type: Pips.Sim.V1.Vec2
end

defmodule Pips.Sim.V1.SpawnPipIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.SpawnPipIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :name, 1, type: :string
  field :position, 2, type: Pips.Sim.V1.Vec2
end

defmodule Pips.Sim.V1.ApplyNeedsIntent.NeedDeltasEntry do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.ApplyNeedsIntent.NeedDeltasEntry",
    map: true,
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :key, 1, type: :int32
  field :value, 2, type: :int32
end

defmodule Pips.Sim.V1.ApplyNeedsIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.ApplyNeedsIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"

  field :need_deltas, 2,
    repeated: true,
    type: Pips.Sim.V1.ApplyNeedsIntent.NeedDeltasEntry,
    json_name: "needDeltas",
    map: true
end

defmodule Pips.Sim.V1.TransferIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.TransferIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :payer_account_id, 1, type: :string, json_name: "payerAccountId"
  field :payee_account_id, 2, type: :string, json_name: "payeeAccountId"
  field :amount, 3, type: :int64
  field :resource_kind, 4, type: :int32, json_name: "resourceKind"
  field :tick, 5, type: :uint64
end

defmodule Pips.Sim.V1.CreditBalancesIntent.Credit do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.CreditBalancesIntent.Credit",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :amount, 2, type: :int64
end

defmodule Pips.Sim.V1.CreditBalancesIntent do
  @moduledoc false

  use Protobuf,
    full_name: "pips.sim.v1.CreditBalancesIntent",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :payer_account_id, 1, type: :string, json_name: "payerAccountId"
  field :credits, 2, repeated: true, type: Pips.Sim.V1.CreditBalancesIntent.Credit
  field :tick, 3, type: :uint64
end
