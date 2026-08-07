# 9. Textures are text, and the browser is the only parser

Status: proposed

## Context

Every visual in `web/` today is a vector drawn at runtime with pixi.js
`Graphics` — `drawBuilding` (`main.ts:225`) hand-codes roof polygons per
building, and pips are circles (`main.ts:473`). There is no bitmap art
anywhere in the project and no pipeline for adding any. That has worked
because there has been exactly one building type worth drawing; the service
map already lists farm and tavern as built and "the rest is the plan" for
workplaces, and each new one currently means someone hand-writing more
polygon code.

That does not scale, and not primarily for a performance reason. An agent
working in this repo can generate and edit text fluently and cannot produce
raster art at all — no eyes, no mouse, no Aseprite. A binary sprite (PNG,
texture atlas) dropped into a PR is also unreviewable the way the rest of
this codebase is reviewable: it does not diff, a reviewer cannot read *what
changed* from the diff, and git cannot merge two edits to it. The project
already treats "can this be reviewed as text" as load-bearing — `proto/` for
contracts, the event log for world history — and rendering assets are the
one visual surface still outside that discipline.

The want is concrete: a workplace or pip should be able to ship a distinct,
textured look — grass, wood grain, ale foam, an idle-vs-working animation —
authored the same way an agent authors everything else here, and reviewed
the same way.

## Decision

### 1. A texture is a plain-text grid, with a palette header

A monospace character grid, one line per row, preceded by a header mapping
each character to a color:

```
palette
  . #00000000
  g #4a7c2f
  G #5c9636
grid
  ..gg..
  .gGGg.
  ..gg..
```

Any character not declared in the palette is a format error, not a
transparent-by-default pixel — an agent that mistypes a glyph should get a
loud failure, not a silently wrong texture.

### 2. Animation is multiple frames in one file, not multiple files

A second section, `frames`, holds an ordered list of grids and an `fps`.
One texture — one visual concept ("tavern, idle") — stays one file whether
it is static or animated, so a diff that adds motion to an existing texture
is a diff to one file, not a rename plus new siblings.

### 3. One parser, one owner: `web/src`

The grid is parsed to an off-screen `<canvas>`, one filled rect per cell,
then handed to pixi as a `Texture`. Nothing outside `web/` ever reads this
format. This is not a contract between services — no `.proto`, no `gen/` —
because it carries no simulation semantics: `sim-core` does not know a
building has a roof texture, only that it is a building. Keeping a second
parser anywhere (say, a native client down the line) would be the same
mistake rule 4 forbids for domain rules, for a different reason: a format
with two readers drifts, and a rendering glitch becomes a cross-language bug
hunt instead of a one-file fix.

### 4. Cell size is a render-time constant, not baked into the file

A texture file declares a grid, not a pixel size. `web/src` decides how many
device pixels a cell occupies, so the same tavern texture can be drawn at
different zoom levels or a future minimap without a second asset.

### 5. Files live under `web/assets/textures/`, validated like code

`{building-or-pip}.{state}.txt`, e.g. `tavern.idle.txt`,
`tavern.working.txt`, `pip.walking.txt`. No manifest service, no registry —
the loader lists the directory. A test wired into `web`'s `make test` (bun
test) rejects a malformed file before it reaches the renderer: non-rectangular
rows, a glyph missing from its palette, or animation frames whose dimensions
disagree with each other. The same bar `make proto-lint` holds contracts to —
a bad file fails the build, not the demo.

## Why not real bitmap assets (PNG / Aseprite / a sprite sheet)

The obvious alternative, and rejected for the reason this ADR exists: an
agent cannot produce or meaningfully edit one, and it does not diff. This
project would still need a human artist, which defeats the point of a
generative pipeline agents can drive end to end.

## Why not procedural/shader-generated textures (noise, WGSL)

More expressive, and premature. There is no shader stage in the renderer
today — pixi is being used purely through `Graphics` — and the actual need
right now is "give ten building types a distinguishable look an agent can
author," not photorealism. Nothing here forecloses adding shaders later; it
just is not the next thing to build.

## Why not keep pure vector polygons

Fine for a building's silhouette — the roof math in `drawBuilding` is not
going away — but it does not extend to surface detail (grass texture, wood
grain, foam) without every such detail becoming more hand-written polygon
code per building type. Text grids and vector shapes are complementary, not
competing: a building keeps its polygon frame and gains a texture fill.

## Why not a shared package parsed by more than one client

There is exactly one client (`web/`). A shared `pips/textures` package
parsed by both a hypothetical native client and the browser is solving a
problem that does not exist yet — add it the day a second consumer does.

## Consequences

- `web/src` gains a parser module and a canvas-to-`Texture` conversion; small,
  but it is new surface area with its own tests.
- Texture count grows per building/pip state, and per animation — the
  filename convention (`{subject}.{state}.txt`) is the only index; if that
  stops being enough (states composing, e.g. "working + injured"), revisit
  before it grows organically into an unreadable naming scheme.
- Pixel-art-scale rendering is now a deliberate style, not a placeholder to
  be embarrassed about. Later polish should stay inside that constraint —
  sharper cells, better palettes — rather than quietly mixing in traditional
  art assets, which would bring back the diffability problem this ADR exists
  to avoid.
- `web/` needs its own `CLAUDE.md` (every other service has one) documenting
  the format for an agent that is about to author a texture, since the spec
  lives in this ADR and an ADR is not where an agent should have to look
  mid-task.

## Next steps

1. Write the format spec precisely (header syntax, `frames`/`fps` syntax,
   escaping) as `web/assets/textures/FORMAT.md` — this ADR sets the shape,
   not the grammar.
2. `web/src/textures.ts` — parser, validator, canvas/`Texture` conversion.
3. A validation test in `web` wired into `make test`, covering the failure
   modes in decision §5.
4. Convert one existing procedural asset (the tavern's wall face is the
   smallest useful target) to prove the pipeline before touching the rest.
5. Add `web/CLAUDE.md` documenting the convention for agents authoring new
   textures.
