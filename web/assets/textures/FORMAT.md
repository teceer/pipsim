# Texture format

See ADR 0009 for why this exists. This is the grammar.

A texture file is plain text, split into sections by lines that name the
section: `palette`, then either `grid` (static) or `frames` (animated).
Leading/trailing blank lines and `#`-comment lines are ignored.

## `palette`

One glyph-to-color mapping per line, indented, glyph then a CSS hex color:

```
palette
  . #00000000
  g #4a7c2f
  G #5c9636
```

- The glyph is exactly one character. Whitespace cannot be a glyph — use a
  dedicated character (conventionally `.`) for "empty".
- Colors are `#rrggbb` or `#rrggbbaa`. `#00000000` is fully transparent.
- Every glyph used anywhere in `grid` or `frames` below must appear here.
  An unknown glyph is a parse error, not a transparent pixel.

## `grid` (static texture)

```
grid
  ..gg..
  .gGGg.
  ..gg..
```

Every row must have the same length. A ragged grid is a parse error.

## `frames` (animation)

```
frames fps=4
  --- 0 ---
  ..gg..
  .gGGg.
  ..gg..
  --- 1 ---
  ..GG..
  .Ggga.
  ..GG..
```

- `frames` takes one required key: `fps`, an integer greater than 0.
- Each frame starts with a `--- <index> ---` marker, indices `0..n-1` in
  order. The marker is discarded; the index is documentation, not read back.
- Every frame's grid must have the same dimensions as the first frame. A
  texture that grows or shrinks mid-animation is a parse error.

A file has exactly one of `grid` or `frames`, never both.

## Naming and location

`web/assets/textures/{subject}.{state}.txt`, e.g. `tavern.idle.txt`,
`pip.walking.txt`. The loader lists the directory; there is no manifest.
