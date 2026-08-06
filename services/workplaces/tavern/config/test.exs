import Config

config :tavern, start_server: false

# Two buildings under test, because one is the case that hides the bugs: with a
# single tavern every id resolves to it and misrouting is invisible. The suite
# checks that they do not share seats and that `Describe` with no id is refused.
config :tavern,
  buildings: [
    %{id: 2, x: 32_000, y: 20_000},
    %{id: 4, x: 40_000, y: 12_000}
  ]

config :logger, level: :warning
