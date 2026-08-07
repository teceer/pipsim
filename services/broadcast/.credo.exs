# Credo, with one check configured rather than disabled.
#
# MissedMetadataKeyInLoggerConfig expects every Logger metadata key to be
# enumerated in config. This project's formatter (Broadcast.LogFormatter)
# serialises whatever a call site passes, precisely so that adding a field to a
# log line is a one-line change rather than two — so the check is pointed at
# :all instead of a list it would otherwise police into existence.
#
# Same file as the tavern's, for the same reason and with the same trade-off.
%{
  configs: [
    %{
      name: "default",
      files: %{included: ["lib/", "test/"]},
      strict: true,
      checks: [
        {Credo.Check.Warning.MissedMetadataKeyInLoggerConfig, metadata_keys: :all}
      ]
    }
  ]
}
