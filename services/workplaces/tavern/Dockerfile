# Build context is the REPOSITORY ROOT, not this directory.
#
# The tavern compiles the checked-in Elixir contracts from gen/elixir straight
# into the release — the same generated tree Go and TypeScript read. That is
# the whole reason the build cannot be scoped to this directory.

FROM hexpm/elixir:1.18.4-erlang-27.3.4-debian-bookworm-20250428-slim AS builder

ENV MIX_ENV=prod

RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends build-essential git \
 && rm -rf /var/lib/apt/lists/*

RUN mix local.hex --force && mix local.rebar --force

WORKDIR /build

# Dependencies first, so editing the tavern's own code does not refetch hex.
COPY services/workplaces/tavern/mix.exs services/workplaces/tavern/mix.lock ./services/workplaces/tavern/
WORKDIR /build/services/workplaces/tavern
RUN mix deps.get --only prod && mix deps.compile

WORKDIR /build
COPY gen/elixir ./gen/elixir
COPY services/workplaces/tavern ./services/workplaces/tavern

WORKDIR /build/services/workplaces/tavern
RUN mix compile && mix release --overwrite

# Not distroless: a BEAM release needs libc, ncurses and openssl at runtime, and
# the runtime image is where the ERTS shipped in the release actually runs.
FROM debian:bookworm-slim AS runtime

RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends libstdc++6 openssl libncurses6 locales ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 65532 tavern

ENV LANG=C.UTF-8

WORKDIR /app
COPY --from=builder --chown=tavern:tavern /build/services/workplaces/tavern/_build/prod/rel/tavern ./

USER tavern
EXPOSE 8090
ENTRYPOINT ["/app/bin/tavern"]
CMD ["start"]
