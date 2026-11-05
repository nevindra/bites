const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseFast });
    const nilo = b.dependency("nilo", .{ .target = target, .optimize = optimize });
    const exe = b.addExecutable(.{
        .name = "nilo",
        .root_module = b.createModule(.{
            .root_source_file = b.path("main.zig"),
            .target = target,
            .optimize = optimize,
            .strip = true, // the Go servers are linked with -s -w
            .imports = &.{.{ .name = "nilo_http", .module = nilo.module("nilo_http") }},
        }),
    });
    b.installArtifact(exe);
}
