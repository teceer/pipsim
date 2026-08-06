defmodule Pips.Events.V1.EventEnvelope do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.EventEnvelope",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  oneof :payload, 0

  field :event_id, 1, type: :string, json_name: "eventId"
  field :tick, 2, type: :uint64
  field :occurred_at, 3, type: Google.Protobuf.Timestamp, json_name: "occurredAt"
  field :trace_id, 4, type: :string, json_name: "traceId"
  field :producer, 5, type: :string
  field :pip_spawned, 10, type: Pips.Events.V1.PipSpawned, json_name: "pipSpawned", oneof: 0

  field :pip_started_work, 11,
    type: Pips.Events.V1.PipStartedWork,
    json_name: "pipStartedWork",
    oneof: 0

  field :pip_ended_work, 12,
    type: Pips.Events.V1.PipEndedWork,
    json_name: "pipEndedWork",
    oneof: 0

  field :pip_got_hungry, 13,
    type: Pips.Events.V1.PipGotHungry,
    json_name: "pipGotHungry",
    oneof: 0

  field :pip_died, 14, type: Pips.Events.V1.PipDied, json_name: "pipDied", oneof: 0

  field :resource_produced, 20,
    type: Pips.Events.V1.ResourceProduced,
    json_name: "resourceProduced",
    oneof: 0

  field :workplace_built, 21,
    type: Pips.Events.V1.WorkplaceBuilt,
    json_name: "workplaceBuilt",
    oneof: 0

  field :workplace_demolished, 22,
    type: Pips.Events.V1.WorkplaceDemolished,
    json_name: "workplaceDemolished",
    oneof: 0
end

defmodule Pips.Events.V1.PipSpawned do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.PipSpawned",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :name, 2, type: :string
  field :position, 3, type: Pips.Sim.V1.Vec2
end

defmodule Pips.Events.V1.PipStartedWork do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.PipStartedWork",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :workplace_id, 2, type: :uint64, json_name: "workplaceId"
  field :workplace_kind, 3, type: :string, json_name: "workplaceKind"
end

defmodule Pips.Events.V1.PipEndedWork do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.PipEndedWork",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :workplace_id, 2, type: :uint64, json_name: "workplaceId"
  field :reason, 3, type: :string
  field :ticks_worked, 4, type: :int32, json_name: "ticksWorked"
end

defmodule Pips.Events.V1.PipGotHungry do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.PipGotHungry",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :food_level, 2, type: :int32, json_name: "foodLevel"
end

defmodule Pips.Events.V1.PipDied do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.PipDied",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :cause, 2, type: :string
  field :age_ticks, 3, type: :int32, json_name: "ageTicks"
end

defmodule Pips.Events.V1.ResourceProduced do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.ResourceProduced",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :pip_id, 2, type: :uint64, json_name: "pipId"
  field :kind, 3, type: Pips.Workplace.V1.ResourceKind, enum: true
  field :amount, 4, type: :int32
end

defmodule Pips.Events.V1.WorkplaceBuilt do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.WorkplaceBuilt",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :kind, 2, type: :string
  field :position, 3, type: Pips.Sim.V1.Vec2
  field :max_workers, 4, type: :int32, json_name: "maxWorkers"
end

defmodule Pips.Events.V1.WorkplaceDemolished do
  @moduledoc false

  use Protobuf,
    full_name: "pips.events.v1.WorkplaceDemolished",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :workplace_id, 1, type: :uint64, json_name: "workplaceId"
  field :reason, 2, type: :string
end
