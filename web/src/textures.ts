/**
 * Parser for the text texture format (ADR 0009, `assets/textures/FORMAT.md`).
 *
 * This is the one place in the project allowed to read that format — keeping
 * a second reader anywhere else would let the two drift, the same failure
 * mode rule 4 forbids for domain rules, applied here to a rendering asset
 * format instead of a simulation one.
 */

import { Texture } from "pixi.js";

export type Palette = Map<string, string>;

export type ParsedTexture =
	| {
			kind: "static";
			palette: Palette;
			width: number;
			height: number;
			grid: string[];
	  }
	| {
			kind: "animated";
			palette: Palette;
			width: number;
			height: number;
			fps: number;
			frames: string[][];
	  };

export class TextureFormatError extends Error {}

function fail(message: string): never {
	throw new TextureFormatError(message);
}

const COLOR_RE = /^#([0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;
const FRAMES_HEADER_RE = /^frames\s+fps=(\d+)$/;
const FRAME_MARKER_RE = /^---\s+(\d+)\s+---$/;

/**
 * Parses one texture file's contents. Throws `TextureFormatError` on any
 * malformed input — a texture an agent got wrong should fail loudly at parse
 * time, not render as a wrong-looking sprite.
 */
export function parseTexture(source: string): ParsedTexture {
	const lines = source
		.split(/\r?\n/)
		.map((line) => line.trim())
		.filter((line) => line.length > 0 && !line.startsWith("#"));

	if (lines[0] !== "palette") {
		fail(
			`expected "palette" as the first section, got ${JSON.stringify(lines[0] ?? "")}`,
		);
	}

	const palette: Palette = new Map();
	let i = 1;
	while (
		i < lines.length &&
		lines[i] !== "grid" &&
		!lines[i].startsWith("frames")
	) {
		const line = lines[i];
		const spaceAt = line.indexOf(" ");
		if (spaceAt < 0) fail(`malformed palette entry: ${JSON.stringify(line)}`);
		const glyph = line.slice(0, spaceAt);
		const color = line.slice(spaceAt + 1).trim();
		if (glyph.length !== 1) {
			fail(
				`palette glyph must be exactly one character: ${JSON.stringify(glyph)}`,
			);
		}
		if (!COLOR_RE.test(color)) {
			fail(
				`palette color must be #rrggbb or #rrggbbaa, got ${JSON.stringify(color)}`,
			);
		}
		palette.set(glyph, color);
		i++;
	}
	if (palette.size === 0) fail("palette section is empty");
	if (i >= lines.length) fail('missing "grid" or "frames" section');

	const checkGlyphs = (row: string) => {
		for (const ch of row) {
			if (!palette.has(ch))
				fail(`glyph ${JSON.stringify(ch)} is not in the palette`);
		}
	};

	if (lines[i] === "grid") {
		const grid = lines.slice(i + 1);
		if (grid.length === 0) fail("grid section is empty");
		const width = grid[0].length;
		for (const row of grid) {
			if (row.length !== width) {
				fail(
					`grid rows must all have the same width (expected ${width}, got ${row.length}: ${JSON.stringify(row)})`,
				);
			}
			checkGlyphs(row);
		}
		return { kind: "static", palette, width, height: grid.length, grid };
	}

	const framesHeader = FRAMES_HEADER_RE.exec(lines[i]);
	if (!framesHeader)
		fail(`malformed section header: ${JSON.stringify(lines[i])}`);
	const fps = Number(framesHeader[1]);
	if (!(fps > 0)) fail(`fps must be greater than 0, got ${fps}`);

	const frames: string[][] = [];
	let j = i + 1;
	let expectedIndex = 0;
	while (j < lines.length) {
		const marker = FRAME_MARKER_RE.exec(lines[j]);
		if (!marker) {
			fail(
				`expected frame marker "--- ${expectedIndex} ---", got ${JSON.stringify(lines[j])}`,
			);
		}
		const idx = Number(marker[1]);
		if (idx !== expectedIndex) {
			fail(
				`frame markers must be sequential from 0, expected ${expectedIndex}, got ${idx}`,
			);
		}
		j++;
		const rows: string[] = [];
		while (j < lines.length && !FRAME_MARKER_RE.test(lines[j])) {
			rows.push(lines[j]);
			j++;
		}
		if (rows.length === 0) fail(`frame ${idx} has no rows`);
		frames.push(rows);
		expectedIndex++;
	}
	if (frames.length === 0) fail("frames section has no frames");

	const width = frames[0][0].length;
	const height = frames[0].length;
	for (const [idx, frame] of frames.entries()) {
		if (frame.length !== height) {
			fail(
				`frame ${idx} has ${frame.length} rows, expected ${height} (frame 0's height)`,
			);
		}
		for (const row of frame) {
			if (row.length !== width) {
				fail(
					`frame ${idx} row width ${row.length} does not match frame 0's width ${width}`,
				);
			}
			checkGlyphs(row);
		}
	}

	return { kind: "animated", palette, width, height, fps, frames };
}

/** One frame's rows, rasterized to a canvas — one filled rect per cell. */
export function frameToCanvas(
	frame: string[],
	palette: Palette,
	cellPx: number,
): HTMLCanvasElement {
	const height = frame.length;
	const width = frame[0]?.length ?? 0;
	const canvas = document.createElement("canvas");
	canvas.width = width * cellPx;
	canvas.height = height * cellPx;
	const ctx = canvas.getContext("2d");
	if (!ctx) throw new Error("2d canvas context unavailable");
	ctx.imageSmoothingEnabled = false;
	for (let y = 0; y < height; y++) {
		const row = frame[y];
		for (let x = 0; x < width; x++) {
			// parseTexture already rejects any glyph missing from the palette, so
			// this lookup cannot miss for a texture that made it through parsing.
			ctx.fillStyle = palette.get(row[x]) as string;
			ctx.fillRect(x * cellPx, y * cellPx, cellPx, cellPx);
		}
	}
	return canvas;
}

/**
 * Every frame of a parsed texture, rasterized to a pixi `Texture` at the
 * given cell size. One frame for a static texture, `fps`-ordered frames for
 * an animation — the cell size is a render-time choice, not part of the file
 * (decision §4), so it is a parameter here rather than baked into `parsed`.
 */
export function texturesFromParsed(
	parsed: ParsedTexture,
	cellPx: number,
): Texture[] {
	const frames = parsed.kind === "static" ? [parsed.grid] : parsed.frames;
	return frames.map((frame) =>
		Texture.from(frameToCanvas(frame, parsed.palette, cellPx)),
	);
}

/** Fetches and parses a texture file from `web/assets/textures/`. */
export async function loadTextureFile(path: string): Promise<ParsedTexture> {
	const res = await fetch(path);
	if (!res.ok)
		throw new Error(
			`failed to load texture ${path}: ${res.status} ${res.statusText}`,
		);
	return parseTexture(await res.text());
}
