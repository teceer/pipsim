# Cluster development loop. Assumes `make infra-up` has already created
# k3d + platform + topics. Tilt owns ONLY our own services — infrastructure
# belongs to Terraform.

k8s_context('k3d-pipsim')
allow_k8s_contexts('k3d-pipsim')

# k3d created this registry and wrote a mirror for it into every node's
# registries.yaml, so `localhost:5050` on the host and `pipsim-registry:5000`
# in the cluster are the same thing.
default_registry('localhost:5050/pipsim')

# --- contracts --------------------------------------------------------------
# A change in proto/ regenerates gen/ once, rather than inside every service.

local_resource(
    'proto-gen',
    cmd='make gen',
    deps=['proto'],
    labels=['contracts'],
)

# --- services ---------------------------------------------------------------
#
# Build contexts are the repository root, not the service directory. Rust reads
# proto/ directly from build.rs, and the TypeScript and Go services consume the
# checked-in bindings in gen/ — none of which live under services/<name>.

docker_build(
    'pipsim/sim-core',
    context='.',
    dockerfile='services/sim-core/Dockerfile',
    only=['proto', 'services/sim-core'],
    ignore=['services/sim-core/target', 'services/sim-core/Dockerfile.dockerignore'],
)

k8s_yaml(helm('infra/helm/sim-core', name='sim-core', namespace='pipsim'))
k8s_resource(
    'sim-core',
    resource_deps=['proto-gen'],
    labels=['services'],
)

# TODO: world-gateway, broadcast, bff, pathfinder and the workplaces land here
# as each grows a Dockerfile and a chart. sim-core is the template.

# --- client -----------------------------------------------------------------
#
# The browser client runs on the host rather than in the cluster: it is static
# assets plus a WASM module, and Vite's own reload loop is faster than anything
# a container round trip could offer.

local_resource(
    'sim-wasm',
    cmd='make -C services/sim-core wasm',
    deps=['services/sim-core/crates/sim', 'services/sim-core/crates/wasm'],
    labels=['client'],
)

local_resource(
    'web',
    serve_cmd='cd web && bun run dev',
    deps=['web/src'],
    resource_deps=['sim-wasm'],
    links=['http://localhost:5173'],
    labels=['client'],
)
