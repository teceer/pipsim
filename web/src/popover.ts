/**
 * The hover card for a building.
 *
 * Plain DOM over the canvas rather than a pixi container, and that is the
 * whole design decision here: the card contains links you click and text you
 * select, and both are free in HTML and hand-written in pixi — hit testing per
 * anchor, a cursor that changes, focus rings, keyboard access. A renderer is
 * for the world; this is chrome on top of it.
 *
 * Nothing in this module imports pixi or reads game state, so it can be
 * reasoned about — and tested — on its own.
 */

import type { Link } from "./links";

export type PopoverContent = {
	/**
	 * Identifies which building this card is for.
	 *
	 * The card is anchored on first appearance and then held still, so moving
	 * to a *different* building has to re-anchor it — otherwise the card stays
	 * where the last one was and silently swaps its contents. Comparing keys is
	 * what tells "same building, fresher numbers" from "new building".
	 */
	key: string;
	title: string;
	subtitle: string;
	/** Label/value rows, rendered in order. */
	facts: [string, string][];
	links: Link[];
	/** Colours the accent bar: a service that failed its health check is red. */
	ok: boolean;
};

const CARD_ID = "pipsim-popover";

/**
 * Builds the card once and reuses it.
 *
 * Reused rather than recreated per hover because the pointer crosses buildings
 * constantly at 60fps, and replacing a subtree that often makes the links
 * unclickable — the node is torn out from under the press.
 */
function element(): HTMLDivElement {
	const existing = document.getElementById(CARD_ID);
	if (existing) return existing as HTMLDivElement;

	const el = document.createElement("div");
	el.id = CARD_ID;
	Object.assign(el.style, {
		position: "fixed",
		zIndex: "10",
		display: "none",
		maxWidth: "300px",
		padding: "10px 12px",
		background: "#161b26",
		border: "1px solid #2c3444",
		borderRadius: "6px",
		color: "#cbd5e1",
		font: "12px/1.5 ui-monospace, monospace",
		boxShadow: "0 8px 24px rgba(0,0,0,.45)",
		// The card must not eat the pointer while it is merely following it,
		// or moving towards a link would count as leaving the building and
		// hide the card. Re-enabled below only when it holds links.
		pointerEvents: "none",
	});
	// Once the pointer is actually on the card, nothing may take it away until
	// the pointer leaves again — not the grace timer, not a stray move event.
	el.addEventListener("mouseenter", () => {
		pointerInside = true;
		clearHide();
	});
	el.addEventListener("mouseleave", () => {
		pointerInside = false;
		hide();
	});

	document.body.appendChild(el);
	return el;
}

const esc = (s: string) =>
	s.replace(
		/[&<>"']/g,
		(c) =>
			({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
				c
			] as string,
	);

/**
 * Pointer-path tracking, so a card you are reaching for does not vanish.
 *
 * The naive version — hide on pointerout — makes every link unclickable: the
 * moment the pointer leaves the building it is over empty ground, and the card
 * it was travelling towards is gone. Adding a plain delay is not much better,
 * because it either hides too fast to cross a gap or lingers over every
 * building you sweep past.
 *
 * The standard fix is the "safe triangle", the one Amazon's menus and macOS
 * submenus use: while the pointer is inside the triangle formed by where it
 * left and the two near corners of the card, it is *heading for the card*, so
 * keep it open. Step outside that wedge and it was going somewhere else.
 */
export type Point = { x: number; y: number };

let hideTimer: number | undefined;
let safeTriangle: [Point, Point, Point] | undefined;
let pointerInside = false;
/** Which building the visible card belongs to, so a move to another re-anchors. */
let shownKey: string | undefined;
/** Last markup written, so an unchanged card is not rebuilt 60 times a second. */
let shownHtml: string | undefined;

// Backstop, in case the pointer stops dead inside the triangle and never
// produces another move event to resolve the question.
const GRACE_MS = 400;

function sign(a: Point, b: Point, c: Point): number {
	return (a.x - c.x) * (b.y - c.y) - (b.x - c.x) * (a.y - c.y);
}

/**
 * Standard half-plane test: inside when all three edge signs agree.
 *
 * Exported for its test. The rest of this module needs a DOM to exercise; this
 * is arithmetic, and it is the part that decides whether the card is reachable
 * — worth pinning on its own.
 */
export function inTriangle(
	p: Point,
	[a, b, c]: [Point, Point, Point],
): boolean {
	const d1 = sign(p, a, b);
	const d2 = sign(p, b, c);
	const d3 = sign(p, c, a);
	const hasNeg = d1 < 0 || d2 < 0 || d3 < 0;
	const hasPos = d1 > 0 || d2 > 0 || d3 > 0;
	return !(hasNeg && hasPos);
}

function clearHide(): void {
	if (hideTimer !== undefined) {
		clearTimeout(hideTimer);
		hideTimer = undefined;
	}
	safeTriangle = undefined;
}

// One listener for the life of the page rather than one per hover: the pointer
// crosses buildings constantly, and add/remove per crossing is both churn and
// a leak waiting to happen.
if (typeof window !== "undefined") {
	window.addEventListener("mousemove", (e) => {
		if (!safeTriangle) return;
		// Left the wedge — it was not coming here after all.
		if (!inTriangle({ x: e.clientX, y: e.clientY }, safeTriangle)) hide();
	});
}

/**
 * Begins hiding, unless the pointer is on its way to the card.
 *
 * Called when the pointer leaves a building. If the card has no links there is
 * nothing to reach for, so it goes immediately.
 */
export function requestHide(x: number, y: number): void {
	const el = document.getElementById(CARD_ID);
	if (!el || el.style.display === "none") return;

	if (el.style.pointerEvents !== "auto") {
		hide();
		return;
	}

	const box = el.getBoundingClientRect();
	// The two corners on the side facing the pointer. Using the near edge
	// rather than all four corners keeps the wedge tight enough that sweeping
	// past a building does not hold its card open.
	const nearX = x < box.left ? box.left : box.right;
	safeTriangle = [
		{ x, y },
		{ x: nearX, y: box.top },
		{ x: nearX, y: box.bottom },
	];

	clearTimeout(hideTimer);
	hideTimer = window.setTimeout(() => {
		if (!pointerInside) hide();
	}, GRACE_MS);
}

export function show(content: PopoverContent, x: number, y: number): void {
	const el = element();
	const accent = content.ok ? "#60a5fa" : "#dc2626";
	clearHide();

	const facts = content.facts
		.map(
			([label, value]) =>
				`<div style="display:flex;gap:10px;justify-content:space-between">
           <span style="color:#7c899c">${esc(label)}</span>
           <span>${esc(value)}</span>
         </div>`,
		)
		.join("");

	// target=_blank with noopener: these open an admin panel, and a tab opened
	// without it keeps a handle on this window through opener.
	const links = content.links
		.map(
			(l) =>
				`<a href="${esc(l.url)}" target="_blank" rel="noopener noreferrer"
            style="display:block;margin-top:6px;color:#7dd3fc;text-decoration:none">
           ${esc(l.label)} ↗
           <div style="color:#6b7688;font-size:11px">${esc(l.hint)}</div>
         </a>`,
		)
		.join("");

	const html = `
    <div style="border-left:3px solid ${accent};padding-left:8px;margin-bottom:6px">
      <div style="color:#e2e8f0;font-weight:600">${esc(content.title)}</div>
      <div style="color:#7c899c;font-size:11px">${esc(content.subtitle)}</div>
    </div>
    ${facts}
    ${links ? `<div style="margin-top:8px;border-top:1px solid #2c3444;padding-top:4px">${links}</div>` : ""}
  `;

	// Rewritten only when it actually differs. The pointer produces a move
	// event per frame, and replacing this subtree 60 times a second tears the
	// anchors out from under a click that is landing on one.
	if (html !== shownHtml) {
		el.innerHTML = html;
		shownHtml = html;
	}

	// Only interactive when there is something to click, so a card with no
	// links never blocks the pointer from reaching the world beneath it.
	el.style.pointerEvents = content.links.length > 0 ? "auto" : "none";

	// Re-anchor when the card appears, or when it is now describing a different
	// building. Not on every move over the same one: following the pointer
	// makes the links unreachable, because the card retreats exactly as fast as
	// you approach it — and the safe triangle above assumes a stationary
	// target, so the two go together.
	const reanchor = el.style.display === "none" || shownKey !== content.key;
	shownKey = content.key;
	el.style.display = "block";

	if (reanchor) {
		// Measured after display, because a hidden element has no size.
		const box = el.getBoundingClientRect();
		const left =
			x + box.width + 16 > window.innerWidth ? x - box.width - 12 : x + 12;
		const top =
			y + box.height + 16 > window.innerHeight ? y - box.height - 12 : y + 12;
		el.style.left = `${Math.max(4, left)}px`;
		el.style.top = `${Math.max(4, top)}px`;
	}
}

export function hide(): void {
	clearHide();
	pointerInside = false;
	shownKey = undefined;
	shownHtml = undefined;
	const el = document.getElementById(CARD_ID);
	if (el) el.style.display = "none";
}
