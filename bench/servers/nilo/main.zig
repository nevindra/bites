//! The nilo server of the harness, the Zig framework bites brings to Go. The same two routes
//! as the Go servers. Validation comes from the handler's types: `:id` is a u32 (a 400 naming it
//! otherwise), `page` is a required query field, `expand` has a default, the JSON body must
//! carry the three members. Differences to note: the id is u32 rather than int64, there is no
//! SIGUSR1 stats hook (the client records RSS from /proc only, and allocations per request are
//! not measured: the allocator is Zig's, not Go's), and there are no goroutines: nilo runs
//! fibers on OS threads, so the process is pinned like the others and uses its own thread count
//! (threads = 0, one per core the process may use).

const std = @import("std");
const nilo = @import("nilo_http");

pub const std_options = nilo.std_options;
pub const std_options_debug_io = nilo.debug_io;
pub const panic = nilo.panic;

const Q = struct {
    expand: bool = false,
    page: u32,
};

const NewUser = struct {
    name: []const u8,
    email: []const u8,
    age: i64,
};

const Created = struct {
    id: u32,
    name: []const u8,
    email: []const u8,
    age: i64,
    page: u32,
};

const User = struct {
    id: u32,
    name: []const u8,
    email: []const u8,
    age: i64,
};

fn create(id: u32, q: nilo.Query(Q), body: NewUser) Created {
    return .{ .id = id, .name = body.name, .email = body.email, .age = body.age, .page = q.value.page };
}

fn get(id: u32) User {
    return .{ .id = id, .name = "ada", .email = "ada@example.dev", .age = 36 };
}

const Blocked = struct { ok: bool };

// GET /block: the block experiment. nilo.sleep parks the fiber for 10 ms (the framework's own
// sleep, which leaves the thread free; std.Thread.sleep would stop every request on it), the
// stand-in for a database call.
fn block() !Blocked {
    try nilo.sleep(10);
    return .{ .ok = true };
}

pub fn main(init: std.process.Init) !void {
    var addr: []const u8 = "127.0.0.1";
    var port: u16 = 19001;
    var args: std.process.Args.Iterator = .init(init.minimal.args);
    _ = args.skip();
    while (args.next()) |a| {
        if (std.mem.eql(u8, a, "-addr")) {
            const v = args.next() orelse return error.MissingValue;
            const i = std.mem.lastIndexOfScalar(u8, v, ':') orelse return error.BadAddr;
            addr = v[0..i];
            port = try std.fmt.parseInt(u16, v[i + 1 ..], 10);
        }
    }

    var app = nilo.App.init(std.heap.smp_allocator);
    defer app.deinit();

    try app.post("/users/:id", create);
    try app.get("/users/:id", get);
    try app.get("/block", block);

    // max_connections defaults to 10 000: the idle run goes to 20 000. max_requests_per_connection
    // defaults to 1000, after which nilo ends the connection; the other servers keep one for ever
    // and the load client does not reconnect, so it is off here.
    try app.listen(.{ .address = addr, .port = port, .max_connections = 40_000, .max_requests_per_connection = 0 });
}
