import { describe, expect, test } from "bun:test";

import {
	type PanelBases,
	basesFromEnv,
	structureLinks,
	workplaceLinks,
} from "./links";

const bases: PanelBases = {
	grafana: "http://grafana.test",
	rabbitmq: "http://rabbit.test",
	redpanda: "http://redpanda.test",
	rabbitmqVhost: "/",
};

describe("basesFromEnv", () => {
	test("defaults match the dev loop, so the feature needs no configuration", () => {
		const b = basesFromEnv({});
		expect(b.grafana).toBe("http://localhost:3001");
		expect(b.rabbitmq).toBe("http://localhost:15672");
		expect(b.redpanda).toBe("http://localhost:8085");
		// compose runs a bare broker; the cluster puts the queues in `pipsim`.
		expect(b.rabbitmqVhost).toBe("/");
	});

	test("the environment overrides every base", () => {
		const b = basesFromEnv({
			VITE_GRAFANA_URL: "https://grafana.example",
			VITE_RABBITMQ_VHOST: "pipsim",
		});
		expect(b.grafana).toBe("https://grafana.example");
		expect(b.rabbitmqVhost).toBe("pipsim");
	});
});

describe("workplaceLinks", () => {
	test("points at the queue this workplace competes on", () => {
		const rabbit = workplaceLinks("farm", bases).find(
			(l) => l.label === "RabbitMQ",
		);
		expect(rabbit?.url).toContain("pipsim.work.farm");
	});

	// A raw `/` in the path resolves to a different route in the management UI
	// and 404s. This is the detail that makes the link work at all.
	test("the default vhost is percent-encoded", () => {
		const rabbit = workplaceLinks("tavern", bases).find(
			(l) => l.label === "RabbitMQ",
		);
		expect(rabbit?.url).toContain("/%2F/");
		expect(rabbit?.url).not.toContain("queues///");
	});

	test("a named vhost survives encoding intact", () => {
		const named = { ...bases, rabbitmqVhost: "pipsim" };
		const rabbit = workplaceLinks("farm", named).find(
			(l) => l.label === "RabbitMQ",
		);
		expect(rabbit?.url).toContain("/pipsim/pipsim.work.farm");
	});
});

describe("structureLinks", () => {
	test("every service gets telemetry, because every service emits it", () => {
		for (const kind of ["bank", "broadcast", "sim-core", "bff"]) {
			expect(
				structureLinks(kind, bases).some((l) => l.label === "Grafana"),
			).toBe(true);
		}
	});

	// Parsed rather than substring-matched: the panes parameter is JSON inside
	// a query string, so the quotes around the service name are escaped twice
	// over and a naive `toContain` asserts the wrong thing about the encoding.
	test("the trace query names the service", () => {
		const grafana = structureLinks("bank", bases).find(
			(l) => l.label === "Grafana",
		);
		const panes = new URL(grafana?.url ?? "").searchParams.get("panes");
		const query = JSON.parse(panes ?? "{}").a.queries[0].query;
		expect(query).toBe('{ .service.name = "bank" }');
	});

	// Only the producers. sim-core has no I/O at all — its facts reach Kafka
	// through the gateway — so linking it to a topic list would be a lie.
	test("only Kafka producers link to the fact log", () => {
		const hasConsole = (kind: string) =>
			structureLinks(kind, bases).some((l) => l.label === "Redpanda Console");

		expect(hasConsole("world-gateway")).toBe(true);
		expect(hasConsole("bank")).toBe(true);
		expect(hasConsole("sim-core")).toBe(false);
		expect(hasConsole("broadcast")).toBe(false);
	});
});

test("an unconfigured tool produces no link rather than a broken one", () => {
	const none: PanelBases = {
		grafana: "",
		rabbitmq: "",
		redpanda: "",
		rabbitmqVhost: "/",
	};
	expect(structureLinks("bank", none)).toEqual([]);
	expect(workplaceLinks("farm", none)).toEqual([]);
});
