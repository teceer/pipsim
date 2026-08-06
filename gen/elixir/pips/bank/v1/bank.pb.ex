defmodule Pips.Bank.V1.TransferKind do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "pips.bank.v1.TransferKind",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :TRANSFER_KIND_UNSPECIFIED, 0
  field :TRANSFER_KIND_WAGE, 1
  field :TRANSFER_KIND_PURCHASE, 2
  field :TRANSFER_KIND_ISSUANCE, 3
end

defmodule Pips.Bank.V1.Account do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.Account",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :id, 1, type: :string
  field :balance, 2, type: :int64
end

defmodule Pips.Bank.V1.OpenAccountRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.OpenAccountRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :account_id, 1, type: :string, json_name: "accountId"
end

defmodule Pips.Bank.V1.OpenAccountResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.OpenAccountResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :account, 1, type: Pips.Bank.V1.Account
end

defmodule Pips.Bank.V1.TransferRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.TransferRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :payer, 1, type: :string
  field :payee, 2, type: :string
  field :amount, 3, type: :int64
  field :kind, 4, type: Pips.Bank.V1.TransferKind, enum: true
  field :tick, 5, type: :uint64
end

defmodule Pips.Bank.V1.TransferResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.TransferResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :ok, 1, type: :bool
  field :reason, 2, type: :string
  field :payer_balance, 3, type: :int64, json_name: "payerBalance"
end

defmodule Pips.Bank.V1.BatchTransferRequest.Credit do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.BatchTransferRequest.Credit",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :payee, 1, type: :string
  field :amount, 2, type: :int64
end

defmodule Pips.Bank.V1.BatchTransferRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.BatchTransferRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :payer, 1, type: :string
  field :credits, 2, repeated: true, type: Pips.Bank.V1.BatchTransferRequest.Credit
  field :kind, 3, type: Pips.Bank.V1.TransferKind, enum: true
  field :tick, 4, type: :uint64
end

defmodule Pips.Bank.V1.BatchTransferResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.BatchTransferResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :ok, 1, type: :bool
  field :reason, 2, type: :string
  field :payer_balance, 3, type: :int64, json_name: "payerBalance"
end

defmodule Pips.Bank.V1.GetBalanceRequest do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.GetBalanceRequest",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :account_id, 1, type: :string, json_name: "accountId"
end

defmodule Pips.Bank.V1.GetBalanceResponse do
  @moduledoc false

  use Protobuf,
    full_name: "pips.bank.v1.GetBalanceResponse",
    protoc_gen_elixir_version: "0.17.0",
    syntax: :proto3

  field :balance, 1, type: :int64
end
