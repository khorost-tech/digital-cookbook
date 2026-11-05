// Проба поведения общей логики: два решения, проверенные исполнением.
//
// ЗАЧЕМ ОНА ЕСТЬ. Проверки исходника держат форму кода, и этого не
// хватило дважды. Сверка имени переживала замену условия на тавтологию
// `variant != variant`, а классификация отказа — дописанное `|| true`:
// текст на месте, решение перестало зависеть от входа. Форму можно
// обойти; ответ на конкретный вход обойти нечем.
//
// ПОЧЕМУ БЕЗ БАЗЫ. Оба решения принимаются ДО похода в базу или вовсе без
// неё: сверка имени — первой строкой ручки, классификация — по коду
// SQLSTATE, который здесь передаётся напрямую. PostgreSQL для этого не
// нужен, и требовать его значило бы сделать проверку непрогоняемой там,
// где она нужнее всего — на машине разбора.
//
// Печатает "PROBE OK" и возвращает 0, если все утверждения сошлись.
#include <atomic>
#include <chrono>
#include <cstdlib>
#include <cstdio>
#include <stdexcept>
#include <string>
#include <thread>

#include "handlers.hpp"

namespace {

int failures = 0;

void check(bool condition, const char* what) {
    if (!condition) {
        std::fprintf(stderr, "НЕ СОШЛОСЬ: %s\n", what);
        ++failures;
    }
}

bool contains(const std::string& haystack, const std::string& needle) {
    return haystack.find(needle) != std::string::npos;
}

}  // namespace

int main() {
#ifdef _WIN32
    ::_putenv_s("STAND_VARIANT", "probe-alpha");
#else
    ::setenv("STAND_VARIANT", "probe-alpha", 1);
#endif
    check(stand_variant() == "probe-alpha",
          "имя варианта читается из окружения");

    // Чужое имя обязано быть ошибкой. Тавтология в сверке проходит мимо
    // проверок формы, но здесь ручка просто не бросит.
    bool threw = false;
    try {
        block(0, "probe-beta", 1);
    } catch (const std::logic_error&) {
        threw = true;
    }
    check(threw, "чужое имя варианта в ручке обязано быть ошибкой");

    // Обратная сторона: своё имя бросать не должно, иначе «сверка»
    // сводится к отказу всегда.
    bool threw_on_own = false;
    try {
        block(0, "probe-alpha", 1);
    } catch (const std::logic_error&) {
        threw_on_own = true;
    }
    check(!threw_on_own, "своё имя варианта обязано проходить сверку");

    // СЧЁТЧИК ЗАНЯТОСТИ. Это измеряющий код внутри самого предмета, и
    // потому его обещания проверяются ВЫЗОВОМ, а не чтением: счётчик,
    // докладывающий константу, не убывающий на выходе или считающий
    // собственный наблюдающий запрос, читается точно так же, как верный.
    check(inflight_now() == 0, "на пустом сервисе в полёте никого");
    {
        InFlight first;
        check(first.entered_with() == 1, "первый вход читается единицей");
        check(inflight_now() == 1, "счётчик вырос на входе");
        {
            InFlight second;
            check(second.entered_with() == 2, "второй вход читается двойкой");
            check(inflight_now() == 2, "два обработчика в полёте разом");
            // Маршрут живости отдаёт занятость и СЕБЯ не считает: двое в
            // полёте — значит двое, а не трое.
            check(contains(healthz().body, "\"inflight\":2"),
                  "маршрут живости отдаёт занятость и себя не считает");
        }
        check(inflight_now() == 1, "счётчик убыл на выходе");
    }
    check(inflight_now() == 0, "счётчик вернулся к нулю");

    // В строку ответа едет ПЕРЕДАННОЕ число, а не выдуманное на месте.
    check(contains(block(0, "probe-alpha", 7).body, "\"inflight\":7"),
          "ручка ожидания кладёт в ответ полученную занятость");

    // Обработчик умеет выйти исключением, и счётчик обязан убыть и тогда:
    // иначе занятость росла бы сама по себе от каждого отказа.
    {
        InFlight guard;
        try {
            (void)block(0, "probe-beta", guard.entered_with());
        } catch (const std::logic_error&) {
        }
    }
    check(inflight_now() == 0,
          "счётчик убывает и когда обработчик вышел исключением");

    // СЧЁТЧИК ОБЩИЙ ДЛЯ ПОТОКОВ, и это не мелочь реализации. Рабочие
    // единицы у двух слоёв из трёх — потоки; счётчик, заведённый на
    // поток, показал бы каждому обработчику одного себя, то есть ровно
    // единицу при любой занятости. Снаружи такой стенд выглядел бы
    // исправным и печатал бы «все три слоя держат по одному запросу
    // разом» — вывод, которого не было. Проверяется вызовом из ДРУГОГО
    // потока: чтением такое не ловится.
    {
        std::atomic<bool> inside{false};
        std::atomic<bool> release{false};
        std::thread other([&inside, &release] {
            InFlight busy;
            inside.store(true);
            while (!release.load()) {
                std::this_thread::sleep_for(std::chrono::milliseconds(1));
            }
        });
        while (!inside.load()) {
            std::this_thread::sleep_for(std::chrono::milliseconds(1));
        }
        check(inflight_now() == 1,
              "занятость, созданная другим потоком, видна в счётчике");
        check(contains(healthz().body, "\"inflight\":1"),
              "маршрут живости видит занятость другого потока");
        {
            InFlight mine;
            check(mine.entered_with() == 2,
                  "вход в другом потоке учтён при входе в этом");
        }
        release.store(true);
        other.join();
    }
    check(inflight_now() == 0, "после ухода потока в полёте никого");

    // Классификация отказа. Два кода — отказ общего ресурса; всё
    // остальное — наша поломка. Произвольный третий код и есть то, чего
    // не удерживает проверка по набору токенов.
    check(is_resource_refusal("53300"), "53300 — отказ общего ресурса");
    check(is_resource_refusal("57P03"), "57P03 — отказ общего ресурса");
    check(!is_resource_refusal("42601"), "42601 — наша поломка (синтаксис)");
    check(!is_resource_refusal("42501"), "42501 — наша поломка (права)");
    check(!is_resource_refusal("28000"), "28000 — наша поломка (вход)");
    check(!is_resource_refusal(""), "пустой код — не отказ ресурса");

    if (failures != 0) {
        std::fprintf(stderr, "не сошлось утверждений: %d\n", failures);
        return 1;
    }
    std::printf("PROBE OK\n");
    return 0;
}
