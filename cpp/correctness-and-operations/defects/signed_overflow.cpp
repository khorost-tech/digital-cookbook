// Ось 1: переполнение знакового целого (неопределённое поведение).
#include <climits>
#include <cstdio>
int main(int argc, char**) {
    int x = INT_MAX - 1 + argc;                         // argc == 1 → INT_MAX
    std::printf("%d\n", x + 1);                         // переполнение знакового
}
