import { describe, expect, test } from "bun:test";

import { type Point, inTriangle } from "./popover";

/**
 * The safe triangle, in the shape it actually takes: the pointer has just left
 * a building at (100, 200), and the card sits to the right, spanning y 150..300
 * with its near edge at x = 300.
 */
const exit: Point = { x: 100, y: 200 };
const wedge: [Point, Point, Point] = [
	exit,
	{ x: 300, y: 150 },
	{ x: 300, y: 300 },
];

describe("the safe triangle keeps a card you are reaching for", () => {
	test("a pointer heading straight at the card stays inside", () => {
		expect(inTriangle({ x: 200, y: 210 }, wedge)).toBe(true);
		expect(inTriangle({ x: 290, y: 250 }, wedge)).toBe(true);
	});

	// The whole reason this exists: the direct path from building to card runs
	// over empty ground, and every point on it has to count as "still coming".
	test("the straight line from exit point to card is inside along its length", () => {
		const target: Point = { x: 300, y: 225 };
		for (let t = 0; t <= 1; t += 0.1) {
			const p = {
				x: exit.x + (target.x - exit.x) * t,
				y: exit.y + (target.y - exit.y) * t,
			};
			expect(inTriangle(p, wedge)).toBe(true);
		}
	});

	test("wandering off perpendicular leaves it", () => {
		// Sharply up, away from the card.
		expect(inTriangle({ x: 150, y: 60 }, wedge)).toBe(false);
		// Sharply down.
		expect(inTriangle({ x: 150, y: 340 }, wedge)).toBe(false);
		// Backwards, away from the card entirely.
		expect(inTriangle({ x: 40, y: 200 }, wedge)).toBe(false);
	});

	// The wedge narrows towards the exit point, so a small sideways move near
	// the building means "not going there" while the same offset near the card
	// is still on course. That taper is the point of a triangle rather than a
	// rectangle: sweeping past a building must not hold its card open.
	test("the wedge is tight near the building and wide near the card", () => {
		expect(inTriangle({ x: 120, y: 240 }, wedge)).toBe(false);
		expect(inTriangle({ x: 280, y: 240 }, wedge)).toBe(true);
	});

	test("the exit point itself counts as inside", () => {
		expect(inTriangle(exit, wedge)).toBe(true);
	});
});

// A card on the left of the pointer produces a wedge wound the other way. The
// half-plane test must not care about winding order, or the triangle works on
// one side of the screen and fails on the other.
test("winding order does not matter", () => {
	const leftward: [Point, Point, Point] = [
		{ x: 500, y: 200 },
		{ x: 300, y: 150 },
		{ x: 300, y: 300 },
	];
	expect(inTriangle({ x: 400, y: 210 }, leftward)).toBe(true);
	expect(inTriangle({ x: 400, y: 20 }, leftward)).toBe(false);
});
