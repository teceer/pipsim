# Cluster development loop. Assumes `make infra-up` has already created
# k3d + platform + topics. Tilt owns ONLY our own services — infrastructure
# belongs to Terraform.

k8s_context('k3d-pipsim')
allow_k8s_contexts('k3d-pipsim')

# --- contracts --------------------------------------------------------------
# A change in proto/ regenerates gen/ once, rather than inside every service.

local_resource(
    'proto-gen',
    cmd='make gen',
    deps=['proto'],
    labels=['contracts'],
)

# --- services ---------------------------------------------------------------

def service(name, path, port_forwards=[], deps=[]):
    docker_build(
        'pipsim/' + name,
        context=path,
        live_update=[sync(path + '/src', '/app/src')],
    )
    k8s_yaml(helm('infra/helm/' + name, name=name, namespace='pipsim'))
    k8s_resource(
        name,
        port_forwards=port_forwards,
        resource_deps=['proto-gen'] + deps,
        labels=['services'],
    )

service('sim-core',      'services/sim-core',      '50051:50051')
service('world-gateway', 'services/world-gateway', '8081:8081', ['sim-core'])
service('broadcast',     'services/broadcast',     '4000:4000', ['world-gateway'])
service('bff',           'services/bff',           '3000:3000', ['world-gateway'])
service('pathfinder',    'services/pathfinder',    '50052:50052')

for wp in ['farm', 'workshop', 'tavern']:
    service(wp, 'services/workplaces/' + wp, [], ['world-gateway'])

# --- client -----------------------------------------------------------------

local_resource(
    'web',
    serve_cmd='cd web && bun run dev',
    deps=['web/src'],
    links=['http://localhost:5173'],
    labels=['client'],
)

# --- observability ----------------------------------------------------------

k8s_resource(
    workload='redpanda-console',
    port_forwards='8080:8080',
    labels=['observability'],
    new_name='kafka-console',
)
