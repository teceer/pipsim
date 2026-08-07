# Build context is the REPOSITORY ROOT, not this directory.
#
# Like the tavern, broadcast compiles the checked-in Elixir contracts from
# gen/elixir straight into the release — it decodes the StreamWorld frames the
# gateway sends, so the generated modules are a build input, not a runtime one.

FROM hexpm/elixir:1.18.4-erlang-27.3.4-debian-bookworm-20250428-slim AS builder

ENV MIX_ENV=prod

RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends build-essential git \
 && rm -rf /var/lib/apt/lists/*

RUN mix local.hex --force && mix local.rebar --force

WORKDIR /build

# Dependencies first, so editing broadcast's own code does not refetch hex.
COPY services/broadcast/mix.exs services/broadcast/mix.lock ./services/broadcast/
WORKDIR /build/services/broadcast
RUN mix deps.get --only prod && mix deps.compile

WORKDIR /build
COPY gen/elixir ./gen/elixir
COPY services/broadcast ./services/broadcast

WORKDIR /build/services/broadcast
RUN mix compile && mix release --overwrite

# Not distroless: a BEAM release needs libc, ncurses and openssl at runtime.
FROM debian:bookworm-slim AS runtime

RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends libstdc++6 openssl libncurses6 locales ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 65532 broadcast

ENV LANG=C.UTF-8

WORKDIR /app
COPY --from=builder --chown=broadcast:broadcast /build/services/broadcast/_build/prod/rel/broadcast ./

# Numeric, not the name — Kubernetes' runAsNonRoot cannot verify a username
# against the image.
USER 65532
EXPOSE 4000
ENTRYPOINT ["/app/bin/broadcast"]
CMD ["start"]
