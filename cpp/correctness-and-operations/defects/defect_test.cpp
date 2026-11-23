// Тест GoogleTest — детектор «gtest» оси 1. Он запускает собранную программу
// с дефектом и проверяет то, что проверил бы обычный тест: код выхода 0 и
// ожидаемую печать там, где она определена. Неопределённое поведение тест не
// предсказывает: для signed_overflow и uninit_read проверяется только форма
// вывода, для data_race — что счёт равен 200000: его программа обязана дать по замыслу.
//
// Каталог с программами — переменная окружения DEFECT_BIN_DIR.
#include <gtest/gtest.h>

#include <array>
#include <cstdio>
#include <cstdlib>
#include <regex>
#include <string>
#include <sys/wait.h>

namespace {

struct Run { int code; std::string out; };

Run run(const std::string& program) {
    const char* dir = std::getenv("DEFECT_BIN_DIR");
    std::string cmd = std::string(dir ? dir : ".") + "/" + program + " 2>/dev/null";
    FILE* p = popen(cmd.c_str(), "r");
    std::string out;
    std::array<char, 256> buf{};
    while (p && fgets(buf.data(), buf.size(), p)) out += buf.data();
    int status = p ? pclose(p) : -1;
    return {WIFEXITED(status) ? WEXITSTATUS(status) : 128 + WTERMSIG(status), out};
}

}  // namespace

TEST(Defect, clean)           { auto r = run("clean");           EXPECT_EQ(r.code, 0); EXPECT_EQ(r.out, "3 42 2000\n"); }
TEST(Defect, heap_overflow)   { auto r = run("heap_overflow");   EXPECT_EQ(r.code, 0); EXPECT_EQ(r.out, "3\n"); }
TEST(Defect, use_after_free)  { auto r = run("use_after_free");  EXPECT_EQ(r.code, 0); EXPECT_EQ(r.out, "42\n"); }
TEST(Defect, signed_overflow) { auto r = run("signed_overflow"); EXPECT_EQ(r.code, 0); EXPECT_TRUE(std::regex_match(r.out, std::regex("-?[0-9]+\n"))); }
TEST(Defect, data_race)       { auto r = run("data_race");       EXPECT_EQ(r.code, 0); EXPECT_EQ(r.out, "200000\n"); }
TEST(Defect, leak)            { auto r = run("leak");            EXPECT_EQ(r.code, 0); EXPECT_EQ(r.out, "1\n"); }
TEST(Defect, uninit_read)     { auto r = run("uninit_read");     EXPECT_EQ(r.code, 0); EXPECT_TRUE(r.out == "one\n" || r.out == "other\n"); }
TEST(Defect, parser_edge)     { auto r = run("parser_edge");     EXPECT_EQ(r.code, 0); EXPECT_EQ(r.out, "1\n"); }
