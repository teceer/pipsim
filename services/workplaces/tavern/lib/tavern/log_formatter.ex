defmodule Tavern.LogFormatter do
  @moduledoc """
  One JSON object per line, with the same keys Go and Rust emit.

  The uniform operational shell in ADR 0003 is only worth anything if it holds
  for the awkward language too. Elixir's metadata is a keyword list of
  arbitrary terms, so anything not directly encodable is inspected rather than
  dropped — a log line that silently loses a field is worse than an ugly one.
  """

  @behaviour :logger_formatter

  @impl true
  def check_config(_config), do: :ok

  @impl true
  def format(%{level: level, msg: msg, meta: meta}, _config) do
    payload =
      %{
        time: timestamp(meta),
        level: level |> to_string() |> String.upcase(),
        msg: message(msg)
      }
      |> Map.merge(fields(meta))

    [Jason.encode_to_iodata!(payload), ?\n]
  end

  defp message({:string, chardata}), do: IO.chardata_to_string(chardata)
  defp message({:report, report}), do: inspect(report)
  defp message({format, args}), do: format |> :io_lib.format(args) |> IO.chardata_to_string()

  defp timestamp(%{time: micros}) do
    micros |> DateTime.from_unix!(:microsecond) |> DateTime.to_iso8601()
  end

  defp timestamp(_), do: DateTime.utc_now() |> DateTime.to_iso8601()

  @dropped [:time, :gl, :pid, :mfa, :file, :line, :domain, :error_logger, :report_cb, :ansi_color]

  defp fields(meta) do
    meta
    |> Map.drop(@dropped)
    |> Map.new(fn {k, v} -> {k, encodable(v)} end)
  end

  defp encodable(v) when is_binary(v) or is_number(v) or is_boolean(v) or is_nil(v), do: v
  defp encodable(v) when is_atom(v), do: to_string(v)
  defp encodable(v), do: inspect(v)
end
