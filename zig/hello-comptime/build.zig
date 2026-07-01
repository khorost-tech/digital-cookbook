const std = @import("std");

// Система сборки Zig — это обычный Zig-код. Проверено на Zig 0.14.
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});

    const exe = b.addExecutable(.{
        .name = "hello-comptime",
        .root_source_file = b.path("main.zig"),
        .target = target,
        .optimize = optimize,
    });
    b.installArtifact(exe);

    // zig build run
    const run_cmd = b.addRunArtifact(exe);
    const run_step = b.step("run", "Собрать и запустить");
    run_step.dependOn(&run_cmd.step);

    // zig build test
    const unit_tests = b.addTest(.{
        .root_source_file = b.path("main.zig"),
        .target = target,
        .optimize = optimize,
    });
    const run_unit_tests = b.addRunArtifact(unit_tests);
    const test_step = b.step("test", "Прогнать тесты");
    test_step.dependOn(&run_unit_tests.step);
}
