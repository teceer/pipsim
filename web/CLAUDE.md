# web

The browser client. Connects to `world-gateway` over Connect (see ADR 0003),
renders the world with pixi.js in 2:1 isometric projection, and runs the same
WASM build of `sim-core` locally for client-side prediction — see the header
comment in `src/main.ts` before touching the render loop.

## Textures

ADR 0009: sprite/roof textures are plain text, not binary images, so an agent
can author or edit one directly. The grammar is `assets/textures/FORMAT.md`;
`src/textures.ts` is the only parser and the only place that format should
ever be read from — don't write a second reader.

Quick shape:

```
palette
  . #00000000
  g #4a7c2f
grid
  .g.
  ggg
  .g.
```

- One file per `{subject}.{state}`, e.g. `tavern.idle.txt`. No manifest — the
  loader in `main.ts` globs `assets/textures/*.txt` at build time
  (`import.meta.glob`).
- Every glyph used in `grid`/`frames` must have a `palette` entry, or parsing
  fails loudly. That is deliberate — a malformed texture should error, not
  silently render a hole.
- `frames fps=N` replaces `grid` for an animation; frames must all share one
  file's dimensions.
- `parseTexture`/`frameToCanvas`/`texturesFromParsed` in `src/textures.ts` are
  plain functions with no pixi or DOM globals at module scope, so
  `textures.test.ts` runs under `bun test` without a browser. Keep it that
  way — if a change needs `document` or a pixi import at module load time,
  it belongs in `main.ts`, not `textures.ts`.
- Cell size (pixels per glyph) is chosen at the call site in `main.ts`, not
  stored in the file — the same texture can render at different zoom levels.

Before shipping a new texture, run `bun test` (validates every file under
`assets/textures/`) and look at it in the running app — a texture that
parses can still look wrong at the chosen cell size.
