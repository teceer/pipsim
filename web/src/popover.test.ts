import { describe, expect, test } from "bun:test";

import { type Point, convexHull, inConvexPolygon } from "./popover";

/**
 * The safe zone as the code builds it: the pointer's exit point plus the four
 * corners of the card, hulled. This is the region "between where I left and
 * what I am reaching for".
 */
function zone(
	exit: Point,
	card: { l: number; t: number; r: number; b: number },
) {
	return convexHull([
		exit,
		{ x: card.l, y: card.t },
		{ x: card.r, y: card.t },
		{ x: card.r, y: card.b },
		{ x: card.l, y: card.b },
	]);
}

/**
 * Every point on the straight line from a to b, sampled.
 *
 * Integer steps rather than accumulating a float: `t += 0.05` twenty times
 * lands slightly past 1, which puts the last sample just outside a target that
 * is a hull vertex — a failure in the test's arithmetic that reads exactly
 * like a failure in the geometry.
 */
function pathIsInside(a: Point, b: Point, poly: Point[]): boolean {
	const steps = 20;
	for (let i = 0; i <= steps; i++) {
		const t = i / steps;
		const p = { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
		if (!inConvexPolygon(p, poly)) return false;
	}
	return true;
}

describe("a card to the side", () => {
	const exit = { x: 100, y: 200 };
	const card = { l: 300, t: 150, r: 500, b: 300 };
	const poly = zone(exit, card);

	test("the direct path to the card stays inside the whole way", () => {
		expect(pathIsInside(exit, { x: 300, y: 225 }, poly)).toBe(true);
		// And to each corner, not just the middle.
		expect(pathIsInside(exit, { x: card.l, y: card.t }, poly)).toBe(true);
		expect(pathIsInside(exit, { x: card.l, y: card.b }, poly)).toBe(true);
	});

	test("the card itself is inside, so arriving on it does not read as leaving", () => {
		expect(inConvexPolygon({ x: 400, y: 220 }, poly)).toBe(true);
		expect(inConvexPolygon({ x: card.r, y: card.b }, poly)).toBe(true);
	});

	test("wandering off perpendicular leaves it", () => {
		expect(inConvexPolygon({ x: 150, y: 40 }, poly)).toBe(false);
		expect(inConvexPolygon({ x: 150, y: 360 }, poly)).toBe(false);
		expect(inConvexPolygon({ x: 20, y: 200 }, poly)).toBe(false);
	});

	// The corridor narrows towards the exit point, so a sideways nudge next to
	// the building means "not going there" while the same offset next to the
	// card is still on course. That taper is what stops a sweep across the map
	// from holding every card it passes open.
	test("the corridor is tight at the building and wide at the card", () => {
		expect(inConvexPolygon({ x: 120, y: 260 }, poly)).toBe(false);
		expect(inConvexPolygon({ x: 290, y: 260 }, poly)).toBe(true);
	});
});

/**
 * The case the previous implementation got wrong.
 *
 * Its triangle's base was always the card's left or right edge, so a card
 * directly above or below the pointer collapsed it into a sliver and the
 * direct path fell outside — the card vanished as you reached for it. This is
 * not hypothetical: the card flips to the other side of the pointer whenever
 * it would overflow a screen edge.
 */
describe("a card directly below the pointer", () => {
	const exit = { x: 400, y: 100 };
	const card = { l: 300, t: 200, r: 500, b: 320 };
	const poly = zone(exit, card);

	test("the direct path down to it stays inside", () => {
		expect(pathIsInside(exit, { x: 400, y: 200 }, poly)).toBe(true);
	});

	test("reaching for either bottom corner stays inside", () => {
		expect(pathIsInside(exit, { x: card.l, y: card.b }, poly)).toBe(true);
		expect(pathIsInside(exit, { x: card.r, y: card.b }, poly)).toBe(true);
	});

	test("moving away upwards still leaves", () => {
		expect(inConvexPolygon({ x: 400, y: 40 }, poly)).toBe(false);
	});
});

describe("a card directly above the pointer", () => {
	const exit = { x: 400, y: 400 };
	const card = { l: 300, t: 150, r: 500, b: 300 };
	const poly = zone(exit, card);

	test("the direct path up to it stays inside", () => {
		expect(pathIsInside(exit, { x: 400, y: 300 }, poly)).toBe(true);
	});
});

describe("convexHull", () => {
	test("keeps only the outline, dropping interior points", () => {
		const hull = convexHull([
			{ x: 0, y: 0 },
			{ x: 10, y: 0 },
			{ x: 10, y: 10 },
			{ x: 0, y: 10 },
			{ x: 5, y: 5 },
		]);
		expect(hull).toHaveLength(4);
		expect(hull).not.toContainEqual({ x: 5, y: 5 });
	});

	// Winding must not matter to the caller: the exit point can land on any
	// side of the card, which flips the order the corners come out in.
	test("the result is usable whichever side the exit point is on", () => {
		const left = zone({ x: 0, y: 225 }, { l: 300, t: 150, r: 500, b: 300 });
		const right = zone({ x: 800, y: 225 }, { l: 300, t: 150, r: 500, b: 300 });
		expect(inConvexPolygon({ x: 200, y: 225 }, left)).toBe(true);
		expect(inConvexPolygon({ x: 600, y: 225 }, right)).toBe(true);
		expect(inConvexPolygon({ x: 600, y: 225 }, left)).toBe(false);
	});
});
