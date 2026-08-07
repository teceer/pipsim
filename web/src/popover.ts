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

export function show(content: PopoverContent, x: number, y: number): void {
	const el = element();
	const accent = content.ok ? "#60a5fa" : "#dc2626";

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

	el.innerHTML = `
    <div style="border-left:3px solid ${accent};padding-left:8px;margin-bottom:6px">
      <div style="color:#e2e8f0;font-weight:600">${esc(content.title)}</div>
      <div style="color:#7c899c;font-size:11px">${esc(content.subtitle)}</div>
    </div>
    ${facts}
    ${links ? `<div style="margin-top:8px;border-top:1px solid #2c3444;padding-top:4px">${links}</div>` : ""}
  `;

	// Only interactive when there is something to click, so a card with no
	// links never blocks the pointer from reaching the world beneath it.
	el.style.pointerEvents = content.links.length > 0 ? "auto" : "none";
	el.style.display = "block";

	// Flip before it runs off the edge. Measured after display, because a
	// hidden element has no size to measure.
	const box = el.getBoundingClientRect();
	const left =
		x + box.width + 16 > window.innerWidth ? x - box.width - 12 : x + 12;
	const top =
		y + box.height + 16 > window.innerHeight ? y - box.height - 12 : y + 12;
	el.style.left = `${Math.max(4, left)}px`;
	el.style.top = `${Math.max(4, top)}px`;
}

export function hide(): void {
	const el = document.getElementById(CARD_ID);
	if (el) el.style.display = "none";
}
