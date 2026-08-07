defmodule Broadcast.LogFormatter do
  @moduledoc """
  One JSON object per line, with the same keys Go and Rust emit.

  A copy of `Tavern.LogFormatter`, and deliberately a copy: the two services
  share no library, and the alternative — a shared Elixir package for forty
  lines of formatting — is a dependency between services that exists so nobody
  has to paste a map. What is shared in this repo is the log *schema*, which is
  written down in ADR 0003, not the code that emits it.
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
