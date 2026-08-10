const std = @import("std");

/// Дженерик через comptime-параметр типа: одна функция для любого T.
/// Тип разрешается на этапе компиляции — никаких макросов и шаблонов.
fn max(comptime T: type, a: T, b: T) T {
    return if (a > b) a else b;
}

pub fn main() void {
    std.debug.print("hello from zig\n", .{});
    std.debug.print("max(i32, 3, 7)    = {d}\n", .{max(i32, 3, 7)});
    std.debug.print("max(f64, 2.5, 1.0) = {d}\n", .{max(f64, 2.5, 1.0)});
}

test "max выбирает большее" {
    try std.testing.expectEqual(@as(i32, 7), max(i32, 3, 7));
    try std.testing.expectEqual(@as(f64, 2.5), max(f64, 2.5, 1.0));
}
