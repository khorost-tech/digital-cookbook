"""Минимальный HTTP/1.1-клиент на asyncio с keep-alive.

Зачем свой, когда есть httpx. Проверка scripts/check-loader.sh показала, что
httpx даёт около 130 запросов в секунду на процесс, и даже четырнадцать ядер
под нагрузчиком не насыщали цель: пропускная способность росла с числом
процессов и не выходила на полку. Значит замер мерил бы клиента.

Поднимать порог проверки было бы подгонкой. Поэтому здесь узкий клиент под
известный сервер: запросы фиксированной формы, соединение переиспользуется.
Редиректы, сжатие и TLS не нужны и не поддержаны.

А вот chunked поддержать пришлось, и это отдельный сюжет. Первая версия
падала на нём намеренно — лучше упасть, чем прочитать неверно. И упала: под
uvicorn Django отдаёт ответы с Transfer-Encoding: chunked, тогда как FastAPI
и голое ASGI-приложение ставят Content-Length. Без поддержки chunked Django
выглядел бы как цель, которая отвечает ошибкой на каждый запрос.

Выравнивать это в самом Django было бы неверно: так он и работает, и лишний
framing — часть его цены, которую замер и должен показать.

Корректность клиента проверяется не на слово: нагрузчик кладёт в результат
хеш тела ответа, и он обязан совпасть с тем, что видит проверка
эквивалентности через стандартный urllib.
"""

import asyncio


class BadResponse(Exception):
    pass


class Connection:
    """Одно keep-alive соединение. Запросы идут строго последовательно."""

    def __init__(self, host, port):
        self.host = host
        self.port = port
        self.reader = None
        self.writer = None

    async def connect(self):
        self.reader, self.writer = await asyncio.open_connection(
            self.host, self.port)
        # Отключаем алгоритм Нейгла: иначе мелкие запросы копятся в буфере и
        # к задержке добавляются десятки миллисекунд, которых нет у сервера.
        sock = self.writer.get_extra_info("socket")
        if sock is not None:
            import socket as _s
            sock.setsockopt(_s.IPPROTO_TCP, _s.TCP_NODELAY, 1)

    async def close(self):
        if self.writer is not None:
            self.writer.close()
            try:
                await self.writer.wait_closed()
            except OSError:
                pass
            self.writer = None

    async def _read_chunked(self):
        """Разбор Transfer-Encoding: chunked.

        Понадобился из-за Django: под uvicorn он отдаёт ответы именно так.
        Трейлеры после нулевого куска не ожидаются и не читаются — сервера
        стенда их не шлют, и молча пропускать неизвестное было бы той же
        ошибкой, что молча читать неподдержанный формат.
        """
        chunks = []
        while True:
            size_line = await self.reader.readline()
            if not size_line:
                raise BadResponse("соединение закрыто посреди chunked-ответа")
            size = int(size_line.split(b";", 1)[0].strip(), 16)
            if size == 0:
                await self.reader.readline()      # завершающий CRLF
                break
            chunks.append(await self.reader.readexactly(size))
            await self.reader.readexactly(2)      # CRLF после куска
        return b"".join(chunks)

    async def request(self, method, path, body=None):
        if self.writer is None:
            await self.connect()

        head = [f"{method} {path} HTTP/1.1", f"Host: {self.host}:{self.port}"]
        if body is not None:
            head.append("Content-Type: application/json")
            head.append(f"Content-Length: {len(body)}")
        else:
            head.append("Content-Length: 0")
        raw = ("\r\n".join(head) + "\r\n\r\n").encode("ascii")
        if body:
            raw += body

        self.writer.write(raw)
        await self.writer.drain()

        status_line = await self.reader.readline()
        if not status_line:
            raise BadResponse("соединение закрыто без ответа")
        try:
            status = int(status_line.split(b" ", 2)[1])
        except (IndexError, ValueError) as exc:
            raise BadResponse(f"нераспознанный статус: {status_line!r}") from exc

        length = None
        keep_alive = True
        chunked = False
        while True:
            line = await self.reader.readline()
            if line in (b"\r\n", b"\n", b""):
                break
            name, _, value = line.decode("latin-1").partition(":")
            name = name.strip().lower()
            value = value.strip()
            if name == "content-length":
                length = int(value)
            elif name == "connection" and value.lower() == "close":
                keep_alive = False
            elif name == "transfer-encoding":
                if value.lower() != "chunked":
                    raise BadResponse(
                        f"неподдерживаемый transfer-encoding: {value}")
                chunked = True

        if chunked:
            payload = await self._read_chunked()
        elif length is not None:
            payload = await self.reader.readexactly(length) if length else b""
        else:
            raise BadResponse("ответ без Content-Length и без chunked")

        if not keep_alive:
            await self.close()
        return status, payload
