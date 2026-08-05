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

def service(name, dockerfile, port_forwards=[], deps=[]):
    docker_build(
        'pipsim/' + name,
        context='.',
        dockerfile=dockerfile,
        only=['proto', 'gen', 'go.work', 'services'],
        ignore=['services/sim-core/target', '**/node_modules'],
    )
    k8s_yaml(helm('infra/helm/' + name, name=name, namespace='pipsim'))
    k8s_resource(
        name,
        port_forwards=port_forwards,
        resource_deps=['proto-gen'] + deps,
        labels=['services'],
    )

service('sim-core', 'services/sim-core/Dockerfile', '50051:50051')
service('farm', 'services/workplaces/farm/Dockerfile', '8090:8090')
service('world-gateway', 'services/world-gateway/Dockerfile', '8081:8081',
        ['sim-core', 'farm'])

# TODO: broadcast, bff, pathfinder and the remaining workplaces land here as
# each grows a Dockerfile and a chart. farm is the template for a workplace.

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
