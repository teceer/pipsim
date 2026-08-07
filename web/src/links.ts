/**
 * Operational links for a building on the map.
 *
 * The map already answers "is the bank up" (ADR 0011). This answers the
 * question that immediately follows — "then show it to me" — by pointing at
 * the panel that knows more: Grafana for traces and metrics, the RabbitMQ
 * management UI for a workplace's offer queue, Redpanda Console for the fact
 * log.
 *
 * ## Why the URLs live here and not in the contract
 *
 * The tempting shape is `repeated Link links` on `Structure`, filled in by the
 * gateway from configuration. It was rejected: it makes sim-core — the
 * deterministic core, which may not so much as read a clock — carry admin
 * panel addresses, and it adds a per-service URL to every environment file for
 * something a template already derives. The client knows each building's
 * `kind`; a base URL per tool is all it needs.
 *
 * If links ever have to differ per instance in a way no template can express,
 * that is when a contract field earns its place.
 *
 * Everything here is a pure function of `kind` and the configured bases, so it
 * is tested without a browser.
 */

/** Base URLs, one per tool. An empty string means "not configured". */
export type PanelBases = {
	grafana: string;
	rabbitmq: string;
	redpanda: string;
	/**
	 * RabbitMQ vhost holding the work queues.
	 *
	 * It genuinely differs by environment and cannot be derived: compose runs a
	 * bare broker where everything lands in `/`, while Terraform layer 20
	 * creates a `pipsim` vhost and puts the queues there. A link built for the
	 * wrong one 404s.
	 */
	rabbitmqVhost: string;
};

export type Link = {
	label: string;
	url: string;
	/** Shown under the link — what you will actually find there. */
	hint: string;
};

/**
 * Reads the bases from Vite's env.
 *
 * Defaults match `make infra-forward`, which is where these ports are already
 * documented for the cluster, and compose, which publishes the same ones
 * locally. A default that matches the dev loop means the feature works with no
 * configuration at all.
 */
export function basesFromEnv(
	env: Record<string, string | undefined>,
): PanelBases {
	return {
		grafana: env.VITE_GRAFANA_URL ?? "http://localhost:3001",
		rabbitmq: env.VITE_RABBITMQ_URL ?? "http://localhost:15672",
		redpanda: env.VITE_REDPANDA_URL ?? "http://localhost:8085",
		// `/` is the compose default. On the cluster this is `pipsim`.
		rabbitmqVhost: env.VITE_RABBITMQ_VHOST ?? "/",
	};
}

/**
 * Links for a service structure.
 *
 * Grafana for anything that emits telemetry, which is everything — the OTel
 * service name is the same string as the structure's `kind`, which is why a
 * template works at all. Redpanda Console only for the services that actually
 * produce to Kafka; pointing every building at the topic list would be noise
 * dressed as information.
 */
export function structureLinks(kind: string, bases: PanelBases): Link[] {
	const links: Link[] = [];

	if (bases.grafana) {
		links.push({
			label: "Grafana",
			url: `${bases.grafana}/explore?schemaVersion=1&panes=${encodeURIComponent(
				JSON.stringify({
					a: {
						datasource: "tempo",
						queries: [{ query: `{ .service.name = "${kind}" }` }],
					},
				}),
			)}`,
			hint: "traces, logs and metrics for this service",
		});
	}

	// Only the producers. sim-core's facts reach Kafka through the gateway —
	// the core has no I/O — so the gateway is the one to link, not the core.
	if (bases.redpanda && (kind === "world-gateway" || kind === "bank")) {
		links.push({
			label: "Redpanda Console",
			url: `${bases.redpanda}/topics`,
			hint: "the fact log this service produces to",
		});
	}

	return links;
}

/**
 * Links for a workplace.
 *
 * The offer queue is the interesting one: `pipsim.work.<kind>` is where pips
 * queue for a job, and its depth is the difference between "nobody wants to
 * work here" and "nobody is answering". The management UI encodes the vhost in
 * the path, and `/` has to be escaped as %2F — a raw slash resolves to a
 * different route and 404s.
 */
export function workplaceLinks(kind: string, bases: PanelBases): Link[] {
	const links: Link[] = [];

	if (bases.grafana) {
		links.push({
			label: "Grafana",
			url: `${bases.grafana}/explore?schemaVersion=1&panes=${encodeURIComponent(
				JSON.stringify({
					a: {
						datasource: "tempo",
						queries: [{ query: `{ .service.name = "${kind}" }` }],
					},
				}),
			)}`,
			hint: "traces, logs and metrics for this workplace",
		});
	}

	if (bases.rabbitmq) {
		// encodeURIComponent, not a literal %2F: the default vhost `/` has to
		// become %2F or the management UI resolves a different route and 404s,
		// and a named vhost has to survive the same encoding untouched.
		const vhost = encodeURIComponent(bases.rabbitmqVhost);
		links.push({
			label: "RabbitMQ",
			url: `${bases.rabbitmq}/#/queues/${vhost}/pipsim.work.${kind}`,
			hint: `offers waiting in pipsim.work.${kind}`,
		});
	}

	return links;
}
