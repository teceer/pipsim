import Config

config :tavern, start_server: false

# Two buildings under test, because one is the case that hides the bugs: with a
# single tavern every id resolves to it and misrouting is invisible. The suite
# checks that they do not share seats and that `Describe` with no id is refused.
config :tavern,
  buildings: [
    %{id: 2},
    %{id: 4}
  ]

config :logger, level: :warning
