"""Раздача локального индекса с заголовками кэширования.

ЗАЧЕМ НЕ ПРОСТО http.server. Штатный SimpleHTTPRequestHandler шлёт только
Last-Modified. Этого достаточно для uv, который держит собственный кэш
артефактов, но НЕ достаточно для pip: его HTTP-кэш построен на
CacheControl и без Cache-Control или ETag ответы не сохраняет.

Замер это увидел так: у pip тёплый прогон почти не отличался от холодного
(10,05 против 10,44 секунды), а каталог кэша оставался пустым — ноль
файлов. Вывод «pip не выигрывает от тёплого кэша» напрашивался сам и был
бы неверным: он не выигрывал от кэша, которого мой индекс не позволял
создать.

Настоящий PyPI отдаёт и ETag, и Cache-Control. Локальный индекс обязан
вести себя так же, иначе условия у инструментов неравны — и неравны
незаметно.
"""

import hashlib
import http.server
import os
import socketserver

PORT = int(os.environ.get("PORT", "8080"))
# Колёса неизменяемы: имя файла содержит версию, и содержимое по этому
# адресу измениться не может. Страницы индекса меняться могут, поэтому им
# отдельный, короткий срок.
IMMUTABLE = 86400 * 365
LISTING = 60


class Handler(http.server.SimpleHTTPRequestHandler):
    def end_headers(self):
        path = self._resolve(self.translate_path(self.path))
        if path and os.path.isfile(path):
            if path.endswith((".whl", ".tar.gz", ".zip")):
                self.send_header("Cache-Control",
                                 f"public, max-age={IMMUTABLE}, immutable")
            else:
                self.send_header("Cache-Control", f"public, max-age={LISTING}")
            self.send_header("ETag", self._etag(path))
        super().end_headers()

    @staticmethod
    def _resolve(path):
        """Каталог -> его index.html.

        Без этого страницы индекса оставались без заголовков: для пути
        вида /simple/six/ os.path.isfile ложно, ветка не срабатывала, и
        кэшировались только колёса. Колёс по объёму больше, поэтому
        недостача была почти незаметна — а страницы влияют на разрешение
        зависимостей, то есть ровно на ту часть, которую и сравниваем.
        """
        if os.path.isdir(path):
            candidate = os.path.join(path, "index.html")
            return candidate if os.path.isfile(candidate) else None
        return path

    @staticmethod
    def _etag(path):
        """Слабый тег по размеру и времени: читать файл целиком ради
        заголовка накладно, а для кэша достаточно устойчивого признака."""
        st = os.stat(path)
        raw = f"{st.st_size}-{int(st.st_mtime)}".encode()
        return '"' + hashlib.sha256(raw).hexdigest()[:16] + '"'

    def log_message(self, *args):
        """Молча: тысячи строк лога на каждый прогон ничего не дают, а
        замедляют раздачу."""


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    with Server(("", PORT), Handler) as httpd:
        httpd.serve_forever()
