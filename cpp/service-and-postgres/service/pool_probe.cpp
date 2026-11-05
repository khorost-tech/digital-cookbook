// Проба поведения пула соединений: правила выдачи, проверенные ИСПОЛНЕНИЕМ.
//
// ЗАЧЕМ ОНА ЕСТЬ. Проверки исходника держат форму кода, и этого уже
// дважды не хватило в этом стенде: сверка имени варианта пережила замену
// условия на тавтологию, классификация отказа — дописанное `|| true`.
// У пула та же беда в худшем виде: «слот возвращается при исключении» —
// утверждение, которое НЕ ВИДНО в тексте вовсе, если сторож заменить
// парой вызовов. Утечка при этом не портит тот замер, в котором
// случилась: соединения копятся между залпами, и следующий прогон читает
// остаток предыдущего как свойство слоя.
//
// ПОЧЕМУ БЕЗ БАЗЫ. Пул — шаблон, и соединение для него — что угодно, что
// умеет создаваться. Здесь подставлен счётчик: правила выдачи от
// PostgreSQL не зависят, а требовать живую базу значило бы сделать эту
// проверку непрогоняемой там, где она нужнее всего — на машине разбора.
//
// ЧЕГО ЭТА ПРОБА НЕ ДОКАЗЫВАЕТ. Что соединений в БАЗЕ ровно столько,
// сколько слотов: это другое утверждение, и оно снимается из
// pg_stat_activity (fixtures/pool.jsonl). Проверка, живущая рядом с
// кодом, слепа заодно с ним — счёт из базы и есть независимый источник.
//
// Печатает "PROBE OK" и возвращает 0, если все утверждения сошлись.
#include <atomic>
#include <chrono>
#include <cstdio>
#include <memory>
#include <stdexcept>
#include <string>
#include <thread>
#include <vector>

#include "pool.hpp"

namespace {

int failures = 0;

void check(bool condition, const char* what) {
    if (!condition) {
        std::fprintf(stderr, "НЕ СОШЛОСЬ: %s\n", what);
        ++failures;
    }
}

// Подставное «соединение»: помнит свой номер и считает живых.
struct Fake {
    explicit Fake(int number) : number(number) { ++alive; }
    ~Fake() { --alive; }
    int number;
    static std::atomic<int> alive;
};

std::atomic<int> Fake::alive{0};

// Фабрика, считающая обращения: переиспользование видно ровно по ней.
struct Counting {
    std::atomic<int> made{0};
    std::unique_ptr<Fake> operator()() {
        return std::make_unique<Fake>(++made);
    }
};

void reuse_does_not_open_twice() {
    Counting factory;
    ConnectionPool<Fake> pool(2, [&factory] { return factory(); });
    {
        ConnectionPool<Fake>::Lease first = pool.acquire();
        check(factory.made == 1, "первая выдача не открыла соединение");
    }
    {
        ConnectionPool<Fake>::Lease second = pool.acquire();
        check(factory.made == 1,
              "вторая выдача открыла НОВОЕ соединение вместо взятого из "
              "набора — это не пул, а прежнее «подключаемся на запрос»");
    }
    check(pool.idle() == 1, "соединение не вернулось в набор");
    check(pool.open() == 1, "в наборе оказалось не одно соединение");
}

void the_slot_comes_back_after_an_exception() {
    Counting factory;
    ConnectionPool<Fake> pool(1, [&factory] { return factory(); });
    try {
        ConnectionPool<Fake>::Lease lease = pool.acquire();
        throw std::runtime_error("отказ посреди запроса");
    } catch (const std::runtime_error&) {
    }
    check(pool.idle() == 1,
          "слот не вернулся после выхода исключением: пул на 1 после первого "
          "же отказа повис бы навсегда");
    // И он ДЕЙСТВИТЕЛЬНО выдаётся снова — а не только числится
    // свободным.
    ConnectionPool<Fake>::Lease again = pool.acquire();
    check(factory.made == 1, "после отказа пул открыл новое соединение, хотя "
                             "прежнее не было объявлено негодным");
}

void a_discarded_connection_does_not_return_to_the_set() {
    Counting factory;
    ConnectionPool<Fake> pool(1, [&factory] { return factory(); });
    {
        ConnectionPool<Fake>::Lease lease = pool.acquire();
        lease.discard();
    }
    check(pool.idle() == 0, "негодное соединение вернулось в набор");
    check(pool.open() == 0, "слот негодного соединения не освободился");
    check(Fake::alive == 0, "негодное соединение осталось жить");
    ConnectionPool<Fake>::Lease fresh = pool.acquire();
    check(factory.made == 2,
          "после негодного соединения пул не открыл новое — значит слот занят "
          "мертвецом, и все следующие запросы отказывали бы подряд");
}

void the_ceiling_holds_under_concurrency() {
    Counting factory;
    const std::size_t size = 3;
    ConnectionPool<Fake> pool(size, [&factory] { return factory(); });
    std::atomic<int> held{0};
    std::atomic<int> peak{0};
    std::vector<std::thread> crowd;
    for (int index = 0; index < 12; ++index) {
        crowd.emplace_back([&pool, &held, &peak] {
            ConnectionPool<Fake>::Lease lease = pool.acquire();
            const int now = ++held;
            int seen = peak.load();
            while (now > seen && !peak.compare_exchange_weak(seen, now)) {
            }
            std::this_thread::sleep_for(std::chrono::milliseconds(20));
            --held;
        });
    }
    for (std::thread& one : crowd) one.join();
    check(peak <= static_cast<int>(size),
          "разом было выдано больше соединений, чем слотов в пуле");
    check(peak == static_cast<int>(size),
          "двенадцать желающих не насытили пул на три — ожидание слота "
          "сериализовало выдачу, и пул на 8 вёл бы себя как пул на 1");
    check(factory.made <= static_cast<int>(size),
          "открыто больше соединений, чем слотов");
}

void a_full_pool_makes_the_caller_wait() {
    Counting factory;
    ConnectionPool<Fake> pool(1, [&factory] { return factory(); });
    std::atomic<bool> got{false};
    ConnectionPool<Fake>::Lease* first =
        new ConnectionPool<Fake>::Lease(pool.acquire());
    std::thread waiting([&pool, &got] {
        ConnectionPool<Fake>::Lease second = pool.acquire();
        got = true;
    });
    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    check(!got, "пул на 1 выдал второе соединение, не дождавшись возврата "
                "первого — потолок не ограничивает ничего");
    delete first;
    waiting.join();
    check(got, "ожидающий не получил слот после возврата");
}

void a_failed_connect_does_not_eat_the_slot() {
    int attempts = 0;
    ConnectionPool<Fake> pool(1, [&attempts]() -> std::unique_ptr<Fake> {
        ++attempts;
        if (attempts == 1) throw std::runtime_error("база не приняла");
        return std::make_unique<Fake>(attempts);
    });
    bool refused = false;
    try {
        ConnectionPool<Fake>::Lease lease = pool.acquire();
    } catch (const std::runtime_error&) {
        refused = true;
    }
    check(refused, "неудачное подключение не доехало до вызывающего");
    check(pool.open() == 0,
          "слот остался забронированным за несостоявшимся соединением: пул на "
          "1 после первого отказа базы не выдал бы больше ничего");
    ConnectionPool<Fake>::Lease lease = pool.acquire();
    check(attempts == 2, "вторая попытка подключиться не состоялась");
}

void an_empty_connection_is_refused() {
    ConnectionPool<Fake> pool(1, [] { return std::unique_ptr<Fake>(); });
    bool refused = false;
    try {
        ConnectionPool<Fake>::Lease lease = pool.acquire();
    } catch (const std::runtime_error&) {
        refused = true;
    }
    check(refused, "пул выдал пустое соединение вместо отказа");
    check(pool.open() == 0, "пустое соединение заняло слот");
}

void a_pool_of_zero_is_refused() {
    bool refused = false;
    try {
        ConnectionPool<Fake> pool(0, [] { return std::make_unique<Fake>(0); });
    } catch (const std::invalid_argument&) {
        refused = true;
    }
    check(refused,
          "пул на ноль слотов создался: первый же запрос повис бы навсегда, и "
          "это выглядело бы зависшим слоем");
}

}  // namespace

int main() {
    reuse_does_not_open_twice();
    the_slot_comes_back_after_an_exception();
    a_discarded_connection_does_not_return_to_the_set();
    the_ceiling_holds_under_concurrency();
    a_full_pool_makes_the_caller_wait();
    a_failed_connect_does_not_eat_the_slot();
    an_empty_connection_is_refused();
    a_pool_of_zero_is_refused();
    if (failures != 0) {
        std::fprintf(stderr, "не сошлось утверждений: %d\n", failures);
        return 1;
    }
    std::printf("PROBE OK\n");
    return 0;
}
