// Проба поведения общего разбора запроса и ручки числа рабочих единиц.
//
// ЗАЧЕМ ОНА ЕСТЬ. Проверки исходника держат форму, и форму обходят:
// `index < workers + 1` стоит рядом с ожидаемым текстом и проверке
// безразличен, а чтение окружения, обёрнутое в try/catch с запасным
// значением, оставляет на месте и вызов, и его порядок. Ответ на
// конкретный вход обойти нечем.
//
// ПОЧЕМУ БЕЗ БАЗЫ. Здесь проверяется только то, что решается ДО похода в
// базу: маршрут, разбор параметра, отказ на кривом входе, чтение числа
// рабочих единиц. Ручка /q с ГОДНЫМ параметром сюда не идёт намеренно —
// она пошла бы в PostgreSQL, а проба обязана работать на машине разбора.
//
// РЕЖИМЫ (аргумент командной строки):
//   dispatch        — разбор запроса; STAND_VARIANT обязан быть задан;
//   workers <N>     — stand_workers() обязан вернуть N;
//   workers-refuse  — stand_workers() обязан бросить.
//
// Печатает "PROBE OK" и возвращает 0, если все утверждения сошлись.
#include <cstdio>
#include <cstdlib>
#include <stdexcept>
#include <string>

#include "dispatch.hpp"
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

void probe_dispatch() {
    const std::string variant = stand_variant();

    const Reply health = dispatch("/healthz", variant);
    check(health.status == 200, "/healthz отвечает 200");
    check(contains(health.body, "\"probe\":\"healthz\""),
          "/healthz отвечает контрактной строкой своей пробы");
    check(contains(health.body, "\"variant\":\"" + variant + "\""),
          "/healthz называет имя варианта, прочитанное при старте");

    // ЗАНЯТОСТЬ ЧИТАЕТСЯ ЧЕРЕЗ РАЗБОР. Сторож входа стоит именно здесь,
    // в общем разборе: снятый оттуда, он оставил бы сервис отвечающим —
    // просто занятость читалась бы всегда нулём, и все три слоя
    // выглядели бы держащими по одному запросу разом. Вызовом это не
    // обойти.
    check(contains(health.body, "\"inflight\":0"),
          "на пустом сервисе маршрут живости читает ноль");

    const Reply blocked = dispatch("/block?ms=0", variant);
    check(blocked.status == 200, "/block с годным параметром отвечает 200");
    check(contains(blocked.body, "\"inflight\":1"),
          "ручка ожидания числится в полёте сама: разбор ставит сторожа");
    {
        // Один обработчик уже в полёте — и разбор обязан это увидеть.
        InFlight busy;
        check(contains(dispatch("/block?ms=0", variant).body,
                       "\"inflight\":2"),
              "второй обработчик читается двойкой");
        check(contains(dispatch("/healthz", variant).body, "\"inflight\":1"),
              "маршрут живости читает занятость, не считая себя");
        // Запрос, отбитый разбором, в полёте не числится: сторож стоит
        // ПОСЛЕ проверки параметра, и ошибка пробы занятости не создаёт.
        (void)dispatch("/block?ms=abc", variant);
        check(contains(dispatch("/healthz", variant).body, "\"inflight\":1"),
              "отбитый разбором запрос занятости не добавил");
    }
    check(contains(dispatch("/healthz", variant).body, "\"inflight\":0"),
          "после выхода обработчиков занятость снова ноль");
    check(contains(blocked.body, "\"probe\":\"block\""),
          "/block отвечает строкой своей пробы");
    check(contains(blocked.body, "\"detail\":\"ms=0\""),
          "/block получил ровно то число, которое стояло в запросе");

    // Параметр среди других и в другом порядке обязан находиться: проба
    // оси не обязана ставить его первым.
    const Reply mixed = dispatch("/block?x=1&ms=7", variant);
    check(contains(mixed.body, "\"detail\":\"ms=7\""),
          "параметр находится не только на первом месте");

    // Похожее имя не должно сходить за искомое: `sleep_ms` внутри
    // `no_sleep_ms` — это другой параметр.
    const Reply lookalike = dispatch("/q?no_sleep_ms=5", variant);
    check(lookalike.status == 400,
          "похожее имя параметра не сходит за искомое");

    // Кривой и отсутствующий параметр — отказ, а НЕ подставленный ноль:
    // проба, попросившая ожидание и получившая ноль, вернулась бы
    // мгновенно и записала бы в фикстуру успешный исход, хотя замер не
    // состоялся.
    const Reply no_value = dispatch("/block", variant);
    check(no_value.status == 400, "/block без параметра — отказ");
    const Reply not_a_number = dispatch("/block?ms=abc", variant);
    check(not_a_number.status == 400, "/block с нечислом — отказ");
    const Reply trailing = dispatch("/block?ms=5x", variant);
    check(trailing.status == 400, "/block с хвостом после числа — отказ");
    const Reply empty_value = dispatch("/q?sleep_ms=", variant);
    check(empty_value.status == 400, "/q с пустым значением — отказ");

    // Неизвестный путь обязан отвечать 404 и телом, которое НЕ является
    // контрактной строкой: правильно оформленная строка легла бы в
    // фикстуру наблюдением, а это ошибка пробы.
    const Reply unknown = dispatch("/nope", variant);
    check(unknown.status == 404, "неизвестный путь — 404");
    check(unknown.body.empty() || unknown.body[0] != '{',
          "тело на неизвестный путь не выдаёт себя за строку фикстуры");
    check(!contains(unknown.body, "\"outcome\""),
          "тело на неизвестный путь не несёт исхода");
}

}  // namespace

int main(int argc, char** argv) {
    const std::string mode = argc > 1 ? argv[1] : "dispatch";
    if (mode == "dispatch") {
        probe_dispatch();
    } else if (mode == "workers") {
        if (argc < 3) {
            std::fprintf(stderr, "режиму workers нужно ожидаемое число\n");
            return 2;
        }
        const int expected = std::atoi(argv[2]);
        int measured = 0;
        try {
            measured = stand_workers();
        } catch (const std::exception& failure) {
            std::fprintf(stderr, "НЕ СОШЛОСЬ: %s\n", failure.what());
            return 1;
        }
        check(measured == expected, "прочитанное число рабочих единиц");
        if (measured != expected) {
            std::fprintf(stderr, "ожидалось %d, прочитано %d\n", expected,
                         measured);
        }
    } else if (mode == "workers-refuse") {
        bool threw = false;
        try {
            (void)stand_workers();
        } catch (const std::runtime_error&) {
            threw = true;
        }
        check(threw, "негодное число рабочих единиц обязано ронять запуск");
    } else {
        std::fprintf(stderr, "неизвестный режим: %s\n", mode.c_str());
        return 2;
    }

    if (failures != 0) {
        std::fprintf(stderr, "не сошлось утверждений: %d\n", failures);
        return 1;
    }
    std::printf("PROBE OK\n");
    return 0;
}
