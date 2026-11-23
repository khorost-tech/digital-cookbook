// Проба: санитайзеры вместе с GoogleTest. Каждый тест зелёный по assert,
// ошибка — только для санитайзера. Какой тест запускать, выбирает --gtest_filter.
#include <gtest/gtest.h>
#include <climits>
#include <thread>
#include <vector>
TEST(Probe, HeapOverflow) { std::vector<int> v(4); volatile int i = 4; EXPECT_GE(v.data()[i], INT_MIN); }
TEST(Probe, SignedOverflow) { volatile int x = INT_MAX; int y = x + 1; EXPECT_NE(y, 0); }
int counter = 0;
TEST(Probe, Race) {
    std::thread a([] { for (int i = 0; i < 100000; i++) counter++; });
    std::thread b([] { for (int i = 0; i < 100000; i++) counter++; });
    a.join(); b.join(); EXPECT_GT(counter, 0);
}
