// Тесты libparse — пример GoogleTest и CTest для статьи 9 и предмет оси 7.
// Значение 65536 тестами сознательно не покрыто: набор проходит по всем
// строкам valid_port, но не по границе, и ошибка P1 (lib/planted.hpp)
// остаётся незамеченной при 100% покрытия строк. Тест на границу живёт
// отдельно — tests/gtest/boundary_test.cpp, это контроль оси 7.
#include "parse.hpp"

#include <gtest/gtest.h>

#include <thread>

using parse::Error;

TEST(Query, DecodesPairs) {
    auto q = parse::parse_query("a=1&b=x%20y");
    ASSERT_TRUE(q);
    ASSERT_EQ(q->params.size(), 2u);
    EXPECT_EQ(q->params[1].second, "x y");
}

TEST(Query, RejectsTrailingPercent) {      
    auto q = parse::parse_query("a=100%");
    ASSERT_FALSE(q);
    EXPECT_EQ(q.error(), Error::bad_escape);
}

TEST(Query, ManyPairs) {                   
    auto q = parse::parse_query("a=1&b=2&c=3&d=4&e=5&f=6");
    ASSERT_TRUE(q);
    EXPECT_EQ(q->params.size(), 6u);
    EXPECT_EQ(q->params[5].first, "f");
}

TEST(Query, RejectsMissingEquals) {
    EXPECT_EQ(parse::parse_query("a=1&b").error(), Error::no_equals);
}

TEST(Port, Parses) {
    EXPECT_EQ(parse::parse_port("443").value(), 443);
    EXPECT_EQ(parse::parse_port("0").value(), 0);
    EXPECT_EQ(parse::parse_port("65535").value(), 65535);
}

TEST(Port, RejectsOutOfRange) {              // 65536 здесь нет — ось 7
    EXPECT_FALSE(parse::parse_port("70000"));
}

TEST(Port, RejectsVeryLong) {              
    EXPECT_FALSE(parse::parse_port("99999999999"));
}

TEST(Port, RejectsNonDigits) {
    EXPECT_FALSE(parse::parse_port("44a"));
}

TEST(Proxy, ParsesTcp4) {
    auto h = parse::parse_proxy_v1("PROXY TCP4 192.168.0.1 192.168.0.11 56324 443\r\n");
    ASSERT_TRUE(h);
    EXPECT_EQ(h->src, "192.168.0.1");
    EXPECT_EQ(h->dst, "192.168.0.11");
    EXPECT_EQ(h->sport, 56324);
    EXPECT_EQ(h->dport, 443);
}

TEST(Proxy, RejectsUnknownProto) {
    EXPECT_EQ(parse::parse_proxy_v1("PROXY UDP4 1.1.1.1 2.2.2.2 1 2\r\n").error(), Error::bad_proto);
}

TEST(Proxy, RejectsMissingCrlf) {
    EXPECT_EQ(parse::parse_proxy_v1("PROXY TCP4 1.1.1.1 2.2.2.2 1 2").error(), Error::bad_prefix);
}

TEST(Counter, TwoThreads) {                
    long before = parse::parsed_total();
    auto work = [] { for (int i = 0; i < 2000; i++) (void)parse::parse_query("a=1"); };
    std::thread t1(work), t2(work);
    t1.join();
    t2.join();
    EXPECT_GT(parse::parsed_total(), before);
}
