defmodule Pips.Work.V1.WorkOffer do
  @moduledoc false

  use Protobuf,
    full_name: "pips.work.v1.WorkOffer",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :tick, 2, type: :uint64
  field :trace_id, 3, type: :string, json_name: "traceId"
end

defmodule Pips.Work.V1.HireOutcome do
  @moduledoc false

  use Protobuf,
    full_name: "pips.work.v1.HireOutcome",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :pip_id, 1, type: :uint64, json_name: "pipId"
  field :workplace_id, 2, type: :uint64, json_name: "workplaceId"
  field :workplace_kind, 3, type: :string, json_name: "workplaceKind"
  field :accepted, 4, type: :bool
  field :reason, 5, type: :string
  field :tick, 6, type: :uint64
  field :trace_id, 7, type: :string, json_name: "traceId"
end
