// cycle_fixed.cpp
//
// Опора 1 (починка): разрываем цикл weak_ptr.
// Одна из связей должна быть НЕ-владеющей. Делаем обратную ссылку
// std::weak_ptr: B --weak--> A. Тогда после reset у Node снова
// use_count == 0, деструкторы вызываются, счётчик = 2, ASan чист.

#include <cstdio>
#include <memory>

struct Node {
    std::shared_ptr<Node> strong;  // forward: strong-ссылка
    std::weak_ptr<Node>   back;     // backward: НЕ-владеющая weak-ссылка
    const char* name;

    static int destroyed;

    explicit Node(const char* n) : name(n) {
        std::printf("Node(%s) constructed\n", name);
    }

    ~Node() {
        ++destroyed;
        std::printf("~Node(%s) destroyed (destroyed=%d)\n", name, destroyed);
    }
};

int Node::destroyed = 0;

int main() {
    {
        auto a = std::make_shared<Node>("A");
        auto b = std::make_shared<Node>("B");

        a->strong = b;    // A --strong--> B
        b->back   = a;    // B --weak----> A   (цикл разорван)

        std::printf("before reset: a.use_count=%ld b.use_count=%ld\n",
                    a.use_count(), b.use_count());

        // Демонстрируем, что weak-ссылку можно временно повысить:
        if (auto locked = b->back.lock()) {
            std::printf("b->back.lock() -> Node(%s) alive, use_count=%ld\n",
                        locked->name, locked.use_count());
        }

        a.reset();   // A: держал только внешний handle (weak не считается) -> умирает
        b.reset();   // B: держал внешний handle + a->strong -> оба ушли -> умирает
    }

    std::printf("destructors called: %d (expected 2)\n", Node::destroyed);
    return 0;
}
