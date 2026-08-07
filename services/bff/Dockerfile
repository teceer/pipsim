# Build context is the REPOSITORY ROOT, not this directory.
#
# The bff is a member of the bun workspace declared in the root package.json,
# alongside gen/ts. Installing from this directory alone resolves nothing: bun
# reads the lockfile and the workspace list from the root, and `@pipsim/gen`
# is a workspace package rather than a registry one.

FROM oven/bun:1.2-alpine AS builder

WORKDIR /build

# Manifests first, so editing source does not refetch the dependency tree.
# Every workspace member's package.json has to be here — bun resolves the whole
# workspace before installing, and a member it cannot read is a hard error even
# though this image only builds one of them.
COPY package.json bun.lock ./
COPY gen/ts/package.json ./gen/ts/
COPY web/package.json ./web/
COPY services/bff/package.json ./services/bff/
COPY services/workplaces/workshop/package.json ./services/workplaces/workshop/

RUN bun install --frozen-lockfile

COPY gen/ts ./gen/ts
COPY services/bff ./services/bff

# --target=bun keeps the bun APIs the source uses; the output is a single file
# that still needs the runtime, which is why the stage below is bun and not
# distroless.
RUN cd services/bff && bun build src/index.ts --target=bun --outdir=dist

FROM oven/bun:1.2-alpine AS runtime

# node_modules comes along: bundling resolves imports but does not inline
# native or optional dependencies, and ioredis and the OTel exporter both
# reach for files at runtime.
WORKDIR /app
COPY --from=builder /build/node_modules ./node_modules
COPY --from=builder /build/services/bff/dist ./dist

# bun's image ships this user at uid 1000. Numeric, because Kubernetes'
# runAsNonRoot cannot verify a username against the image.
USER 1000

ENV NODE_ENV=production
EXPOSE 3000
ENTRYPOINT ["bun", "run", "/app/dist/index.js"]
