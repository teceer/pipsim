//! Flow field computation.
//!
//! With hundreds of pips walking towards a handful of destinations, per-pip A*
//! recomputes the same work repeatedly. A flow field runs one BFS from the
//! destination across the whole grid; every pip then reads the direction out of
//! its own cell in O(1).
//!
//! The FFI boundary is deliberately narrow — flat integer slices in and out, no
//! structs. Zig is pre-1.0 and its struct layout rules may shift between
//! releases; flat arrays cannot break silently.

const std = @import("std");

pub const Dir = enum(u8) {
    none = 0,
    n = 1,
    ne = 2,
    e = 3,
    se = 4,
    s = 5,
    sw = 6,
    w = 7,
    nw = 8,
};

const NEIGHBOURS = [8][2]i32{
    .{ 0, -1 }, .{ 1, -1 }, .{ 1, 0 }, .{ 1, 1 },
    .{ 0, 1 },  .{ -1, 1 }, .{ -1, 0 }, .{ -1, -1 },
};

/// Computes the integration field (cost to destination) and the direction
/// field, given a cost grid where 255 means impassable.
///
/// `costs`, `integration` and `directions` are all width*height long. The
/// caller owns all three; this function allocates only scratch space, from an
/// arena it resets before returning.
pub fn compute(
    allocator: std.mem.Allocator,
    width: u32,
    height: u32,
    costs: []const u8,
    dest_x: u32,
    dest_y: u32,
    integration: []u32,
    directions: []u8,
) !void {
    const len = width * height;
    std.debug.assert(costs.len == len);
    std.debug.assert(integration.len == len);
    std.debug.assert(directions.len == len);

    @memset(integration, std.math.maxInt(u32));
    @memset(directions, @intFromEnum(Dir.none));

    // Arena for the frontier queue: allocated once, freed wholesale on return.
    // This is the pattern Zig is here to demonstrate — no per-node allocation,
    // no GC, and a single deterministic release point.
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var queue = try std.ArrayList(u32).initCapacity(scratch, len / 4);

    const dest_idx = dest_y * width + dest_x;
    integration[dest_idx] = 0;
    try queue.append(scratch, dest_idx);

    // BFS outward from the destination, relaxing costs.
    var head: usize = 0;
    while (head < queue.items.len) : (head += 1) {
        const idx = queue.items[head];
        const x: i32 = @intCast(idx % width);
        const y: i32 = @intCast(idx / width);
        const here = integration[idx];

        for (NEIGHBOURS) |d| {
            const nx = x + d[0];
            const ny = y + d[1];
            if (nx < 0 or ny < 0 or nx >= width or ny >= height) continue;

            const nidx: u32 = @intCast(@as(u32, @intCast(ny)) * width + @as(u32, @intCast(nx)));
            const cost = costs[nidx];
            if (cost == 255) continue; // impassable

            const candidate = here + cost;
            if (candidate < integration[nidx]) {
                integration[nidx] = candidate;
                try queue.append(scratch, nidx);
            }
        }
    }

    // Second pass: each cell points at its cheapest neighbour.
    var i: u32 = 0;
    while (i < len) : (i += 1) {
        if (integration[i] == std.math.maxInt(u32)) continue;

        const x: i32 = @intCast(i % width);
        const y: i32 = @intCast(i / width);
        var best: u32 = integration[i];
        var best_dir: u8 = @intFromEnum(Dir.none);

        for (NEIGHBOURS, 0..) |d, k| {
            const nx = x + d[0];
            const ny = y + d[1];
            if (nx < 0 or ny < 0 or nx >= width or ny >= height) continue;

            const nidx: u32 = @intCast(@as(u32, @intCast(ny)) * width + @as(u32, @intCast(nx)));
            if (integration[nidx] < best) {
                best = integration[nidx];
                best_dir = @intCast(k + 1);
            }
        }
        directions[i] = best_dir;
    }
}

/// C ABI entry point called from Rust. Returns 0 on success.
export fn pipsim_flowfield_compute(
    width: u32,
    height: u32,
    costs: [*]const u8,
    dest_x: u32,
    dest_y: u32,
    integration: [*]u32,
    directions: [*]u8,
) callconv(.c) i32 {
    const len = width * height;
    compute(
        std.heap.page_allocator,
        width,
        height,
        costs[0..len],
        dest_x,
        dest_y,
        integration[0..len],
        directions[0..len],
    ) catch return -1;
    return 0;
}

test "flow field points towards the destination" {
    const w: u32 = 8;
    const h: u32 = 8;
    const allocator = std.testing.allocator;

    const costs = try allocator.alloc(u8, w * h);
    defer allocator.free(costs);
    @memset(costs, 1);

    const integration = try allocator.alloc(u32, w * h);
    defer allocator.free(integration);
    const directions = try allocator.alloc(u8, w * h);
    defer allocator.free(directions);

    try compute(allocator, w, h, costs, 0, 0, integration, directions);

    // The destination itself costs nothing and points nowhere.
    try std.testing.expectEqual(@as(u32, 0), integration[0]);
    try std.testing.expectEqual(@intFromEnum(Dir.none), directions[0]);

    // Every other reachable cell has a finite cost and a direction to follow.
    for (1..w * h) |i| {
        try std.testing.expect(integration[i] < std.math.maxInt(u32));
        try std.testing.expect(directions[i] != @intFromEnum(Dir.none));
    }
}

test "impassable cells stay unreachable" {
    const w: u32 = 4;
    const h: u32 = 1;
    const allocator = std.testing.allocator;

    var costs = [_]u8{ 1, 255, 1, 1 };
    const integration = try allocator.alloc(u32, w * h);
    defer allocator.free(integration);
    const directions = try allocator.alloc(u8, w * h);
    defer allocator.free(directions);

    try compute(allocator, w, h, &costs, 0, 0, integration, directions);

    try std.testing.expectEqual(@as(u32, std.math.maxInt(u32)), integration[1]);
    // The wall cuts the row in two, so cells beyond it are unreachable too.
    try std.testing.expectEqual(@as(u32, std.math.maxInt(u32)), integration[2]);
}
