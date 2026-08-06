import { describe, expect, test } from "bun:test";
import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";
import { TextureFormatError, parseTexture } from "./textures";

const TEXTURES_DIR = join(import.meta.dir, "..", "assets", "textures");

const VALID_STATIC = `
palette
  . #00000000
  g #4a7c2f
grid
  .g.
  ggg
  .g.
`;

const VALID_ANIMATED = `
palette
  . #00000000
  g #4a7c2f
frames fps=4
  --- 0 ---
  .g
  g.
  --- 1 ---
  g.
  .g
`;

describe("parseTexture", () => {
	test("parses a static grid", () => {
		const parsed = parseTexture(VALID_STATIC);
		expect(parsed.kind).toBe("static");
		if (parsed.kind !== "static") return;
		expect(parsed.width).toBe(3);
		expect(parsed.height).toBe(3);
		expect(parsed.palette.get("g")).toBe("#4a7c2f");
	});

	test("parses animated frames in fps order", () => {
		const parsed = parseTexture(VALID_ANIMATED);
		expect(parsed.kind).toBe("animated");
		if (parsed.kind !== "animated") return;
		expect(parsed.fps).toBe(4);
		expect(parsed.frames).toHaveLength(2);
		expect(parsed.width).toBe(2);
		expect(parsed.height).toBe(2);
	});

	test("rejects a ragged grid", () => {
		const source = `
palette
  . #000000
  g #4a7c2f
grid
  .g.
  gg
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects a glyph missing from the palette", () => {
		const source = `
palette
  . #000000
grid
  .x.
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects an animation frame with mismatched dimensions", () => {
		const source = `
palette
  . #000000
  g #4a7c2f
frames fps=2
  --- 0 ---
  .g
  g.
  --- 1 ---
  .g.
  g..
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects out-of-order frame markers", () => {
		const source = `
palette
  . #000000
frames fps=2
  --- 0 ---
  .
  --- 2 ---
  .
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects fps=0", () => {
		const source = `
palette
  . #000000
frames fps=0
  --- 0 ---
  .
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects a missing grid/frames section", () => {
		const source = `
palette
  . #000000
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects a palette glyph longer than one character", () => {
		const source = `
palette
  gg #000000
grid
  gg
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});

	test("rejects a malformed color", () => {
		const source = `
palette
  . red
grid
  .
`;
		expect(() => parseTexture(source)).toThrow(TextureFormatError);
	});
});

describe("shipped texture assets", () => {
	test("every file under assets/textures/ parses", async () => {
		const entries = await readdir(TEXTURES_DIR);
		const textureFiles = entries.filter((name) => name.endsWith(".txt"));
		expect(textureFiles.length).toBeGreaterThan(0);
		for (const name of textureFiles) {
			const source = await readFile(join(TEXTURES_DIR, name), "utf8");
			expect(() => parseTexture(source), name).not.toThrow();
		}
	});
});
