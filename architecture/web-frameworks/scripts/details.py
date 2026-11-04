"""Человекочитаемый detail строки фикстуры: из признаков actual, без шумных полей."""

NAMES = {"go-nethttp": "net/http", "go-gin": "Gin", "rust-axum": "axum", "rust-actix": "actix-web", "py-flask": "Flask",
         "py-fastapi": "FastAPI", "node-express": "Express", "node-nestjs": "NestJS", "java-javalin": "Javalin",
         "java-spring": "Spring Boot", "cs-minimal": "Minimal API", "cs-mvc": "ASP.NET MVC"}


def yes(b):
    return "да" if b else "нет"


def by_app(d, fmt=str):
    return ", ".join("%s — %s" % (NAMES[k], fmt(v)) for k, v in d.items())


def code(c):
    return "нет ответа" if c == 0 else str(c)


def opt(v):
    return "—" if v is None else ("да" if v is True else "нет" if v is False else code(v))


def describe(key, a, extra):
    if key == "control":
        return ("корректный POST: %s; name в ответе: %s; GET: %s; id в ответе: %s; Javalin со штатным валидатором, GET /typed/items/1: %s") % (
            by_app(a["valid_post"], code), by_app(a["valid_post_echoes_name"], yes), by_app(a["valid_get"], code),
            by_app(a["valid_get_returns_id"], yes), yes(a["javalin_typed_valid_get"]))
    if key == "routing":
        return ("DELETE на существующий путь: %s; Allow при 405: %s; GET со слэшем в конце: %s; POST со слэшем в конце: %s; "
                "тело дошло при повторе POST по Location: %s; id=abc: %s; id=1abc: %s; id=0: %s; неизвестный путь: %s; "
                "Javalin со штатным валидатором, id=abc: %s") % (
            by_app(a["wrong_method"], code), by_app(a["allow_present"], opt), by_app(a["trailing_slash"], code),
            by_app(a["post_trailing_slash"], code), by_app(a["post_redirect_body_kept"], opt), by_app(a["bad_param"], code),
            by_app(a["digits_prefix_param"], code), by_app(a["zero_id"], code), by_app(a["unknown_path"], code), code(a["javalin_typed_bad_param"]))
    if key == "validation":
        return ("битый JSON: %s; без name: %s; name пустой строкой: %s; qty строкой \"many\": %s; qty числовой строкой \"1\": %s; "
                "qty дробью 1.5: %s; мусор после JSON: %s") % (
            by_app(a["malformed_json"], code), by_app(a["missing_name"], code), by_app(a["empty_name"], code), by_app(a["wrong_type"], code),
            by_app(a["numeric_string"], code), by_app(a["fractional"], code), by_app(a["trailing_garbage"], code))
    if key == "limits":
        return "; ".join("тело %s байт: %s" % (size, by_app(v, code)) for size, v in a["by_size"].items())
    if key == "errors":
        return ("исключение в обработчике: %s; транспорт: %s; сообщение или стек в теле: %s; сервер раскрывает себя в заголовках: %s; "
                "контроли детектора утечки: %s") % (
            by_app(a["boom_status"], code), by_app(a["boom_transport"]), by_app(a["boom_leaks_message_or_stack"], yes),
            by_app(a["server_discloses_software"], yes), "сошлись" if a["detector_controls_ok"] else "не сошлись")
    if key == "footprint":
        return ("старт до первого ответа, медиана трёх запусков: Spring Boot дольше Javalin больше чем в 1,5 раза — %s; "
                "Gin и net/http в пределах двух раз — %s") % (yes(a["java_full_starts_over_1_5x_slower"]), yes(a["go_pair_within_2x"]))
    return ""
