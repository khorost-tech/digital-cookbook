// Тот же сервис стенда, что и service/main.cpp, на Rust.
//
// ПОЧЕМУ ЭТО ВАЖНО ДОСЛОВНО. Ось сравнивает размеры трёх бинарников, и
// сравнение имеет смысл ровно до тех пор, пока это три реализации ОДНОГО
// сервиса. Ответ HTTP собирается вручную, теми же заголовками в том же
// порядке, и JSON пробы несёт те же ключи в том же порядке. Проверка
// тождества на оси сравнивает полный ответ побайтно.
//
// ПОЧЕМУ kind РАВЕН "package". Это контракт ПРОБЫ сервиса, общий с
// C++-версией; строку фикстуры с kind="sibling" пишет измеритель, а не
// сервис. Правка kind здесь развалила бы главный контроль оси.
//
// ПОЧЕМУ БЕЗ ЕДИНОЙ ВНЕШНЕЙ ЗАВИСИМОСТИ. Пустой раздел [dependencies] —
// объявление, а не доказательство: его написал я. Доказывает сборка с
// --offline при CARGO_NET_OFFLINE=true, где любой внешний крейт уронил
// бы её.
use std::env;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream, ToSocketAddrs};

fn json_escape(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for ch in value.bytes() {
        match ch {
            b'"' => out.push_str("\\\""),
            b'\\' => out.push_str("\\\\"),
            b'\n' => out.push_str("\\n"),
            b'\r' => out.push_str("\\r"),
            b'\t' => out.push_str("\\t"),
            c if c < 0x20 => out.push_str(&format!("\\u{:04x}", c)),
            c => out.push(c as char),
        }
    }
    out
}

fn json_line(probe: &str, outcome: &str, detail: &str) -> String {
    let variant = env::var("STAND_VARIANT").unwrap_or_else(|_| "unknown".into());
    format!(
        "{{\"kind\":\"package\",\"variant\":\"{}\",\"probe\":\"{}\",\
         \"outcome\":\"{}\",\"detail\":\"{}\"}}",
        json_escape(&variant),
        probe,
        outcome,
        json_escape(detail)
    )
}

// Повторяет смысл resolve() из service/main.cpp: имя либо разрешилось,
// либо нет. ToSocketAddrs в std идёт через getaddrinfo той же libc, с
// которой слинкован бинарник, — то есть через musl, как и у C++-плеча.
// Текст ошибки у каждой реализации свой, и разбор сравнивает исход, а не
// текст.
fn resolve(host: &str) -> String {
    match (host, 80u16).to_socket_addrs() {
        Ok(mut addrs) => {
            if addrs.next().is_some() {
                json_line("resolve", "resolved", "")
            } else {
                json_line("resolve", "refused", "пустой список адресов")
            }
        }
        Err(err) => json_line("resolve", "refused", &err.to_string()),
    }
}

fn query_value(target: &str, key: &str) -> String {
    let needle = format!("{}=", key);
    match target.find(&needle) {
        None => String::new(),
        Some(pos) => {
            let rest = &target[pos + needle.len()..];
            match rest.find(['&', ' ']) {
                None => rest.to_string(),
                Some(end) => rest[..end].to_string(),
            }
        }
    }
}

fn serve(mut conn: TcpStream) {
    let mut buf = [0u8; 2048];
    let n = conn.read(&mut buf).unwrap_or(0);
    let mut body = String::from("ok");
    if n > 0 {
        let request = String::from_utf8_lossy(&buf[..n]).to_string();
        if request.starts_with("GET /resolve") {
            let host = query_value(&request, "host");
            let host = if host.is_empty() { "example.com" } else { &host };
            body = resolve(host);
        }
    }
    let response = format!(
        "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\
         Content-Length: {}\r\nConnection: close\r\n\r\n{}",
        body.len(),
        body
    );
    let _ = conn.write_all(response.as_bytes());
}

fn main() {
    let mut port = String::from("8080");
    for arg in env::args().skip(1) {
        if let Some(value) = arg.strip_prefix("--port=") {
            port = value.to_string();
        }
    }

    let listener = match TcpListener::bind(format!("0.0.0.0:{}", port)) {
        Ok(listener) => listener,
        Err(_) => {
            eprintln!("не занять порт {}", port);
            std::process::exit(2);
        }
    };
    // Строка готовности — та же, что у C++ и Go: прогон ждёт её, а не
    // спит по таймеру.
    eprintln!("READY {}", port);

    for conn in listener.incoming() {
        match conn {
            Ok(conn) => serve(conn),
            Err(_) => continue,
        }
    }
}
