// Контроль оси 7: тест на границу 65536. С внесённой ошибкой P1 обязан
// упасть, без неё — пройти. В основной набор (parse_test) не входит: там
// граница сознательно не покрыта, и это предмет оси.
#include "parse.hpp"

#include <gtest/gtest.h>

TEST(Port, RejectsFirstValueAboveRange) {
    EXPECT_FALSE(parse::valid_port(65536));
    EXPECT_FALSE(parse::parse_port("65536"));
}
