// cycle_leak.cpp
//
// Опора 1 (провал): цикл из двух shared_ptr ТЕЧЁТ.
// Подсчёт ссылок не умеет собирать циклы: A держит strong-ссылку на B,
// B держит strong-ссылку на A. После a.reset()/b.reset() внешние
// handle'ы уходят, но use_count каждого Node остаётся 1 (держит сосед).
// Деструкторы НЕ вызываются -> счётчик деструкторов = 0 -> утечка.
//
// Сборка с -fsanitize=address заставляет LeakSanitizer на выходе
// отчитаться "detected memory leaks" на аллокациях Node.
//
// Замечание про LeakSanitizer: LSan консервативно сканирует стек как
// набор корней. Если сырое значение указателя на Node останется в
// старом стековом кадре, LSan сочтёт объект достижимым и утечку НЕ
// покажет (ложноотрицательный результат). Поэтому цикл строим в
// отдельной функции make_cycle() (её кадр снимается при возврате),
// а перед выходом затираем стек clobber_stack(), чтобы убрать
// остаточные указатели. Сама утечка при этом настоящая — это лишь
// защита от корневого сканера LSan.

#include <cstdio>
#include <memory>

struct Node {
    std::shared_ptr<Node> other;   // ВЗАИМНАЯ strong-ссылка -> цикл
    const char* name;

    static int destroyed;          // статический счётчик деструкторов

    explicit Node(const char* n) : name(n) {
        std::printf("Node(%s) constructed\n", name);
    }

    ~Node() {
        ++destroyed;
        std::printf("~Node(%s) destroyed (destroyed=%d)\n", name, destroyed);
    }
};

int Node::destroyed = 0;

__attribute__((noinline))
static void make_cycle() {
    auto a = std::make_shared<Node>("A");
    auto b = std::make_shared<Node>("B");

    a->other = b;   // A --strong--> B
    b->other = a;   // B --strong--> A   (замкнули цикл)

    std::printf("before reset: a.use_count=%ld b.use_count=%ld\n",
                a.use_count(), b.use_count());

    a.reset();      // сбросили внешний handle на A
    b.reset();      // сбросили внешний handle на B
    // Здесь A и B всё ещё живы: держат друг друга (use_count == 1).
}

// Затираем старый стековый кадр, чтобы остаточные сырые указатели на
// Node не были замечены корневым сканером LeakSanitizer.
__attribute__((noinline))
static void clobber_stack() {
    volatile char buf[8192];
    for (int i = 0; i < 8192; ++i) buf[i] = static_cast<char>(i & 0x7f);
    (void)buf;
}

int main() {
    make_cycle();
    clobber_stack();

    std::printf("destructors called: %d (expected 2, but cycle leaks -> 0)\n",
                Node::destroyed);
    // LeakSanitizer вызывает _exit() после проверки утечек, минуя сброс
    // буферов stdio -> явно сбрасываем, иначе строки выше потеряются.
    std::fflush(stdout);
    return 0;
}
