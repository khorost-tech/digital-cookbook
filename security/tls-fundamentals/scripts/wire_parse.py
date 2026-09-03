"""Разбор приветствия клиента: что видно на проводе до всякого шифрования.

САМООБМАН, ОТ КОТОРОГО ЗДЕСЬ ЗАЩИЩАЮТСЯ.

Имя сервера мы сами передали клиенту в аргументах. Напечатать его и
назвать наблюдением — проще всего и совершенно бессмысленно. Поэтому
разбор возвращает не только имя, но и СМЕЩЕНИЕ, по которому оно найдено
в байтах, а проверка отдельно убеждается, что байты по этому смещению
действительно складываются в это имя.

Разбор идёт по структуре записи, а не поиском подстроки: найденная
подстрока могла бы оказаться где угодно, а нам важно, что имя лежит
именно в поле имени и читается любым, кто видит провод.
"""


class WireError(Exception):
    """Байты не разбираются как приветствие клиента."""


def _u8(data, i):
    return data[i], i + 1


def _u16(data, i):
    return (data[i] << 8) | data[i + 1], i + 2


def _u24(data, i):
    return (data[i] << 16) | (data[i + 1] << 8) | data[i + 2], i + 3


def parse_client_hello(data):
    """Разбирает приветствие клиента и возвращает найденное.

    Возвращает словарь с полями:
      record_version  — версия, объявленная в заголовке записи;
      hello_version   — версия, объявленная в самом приветствии;
      server_name     — запрошенное имя (или "");
      server_name_at  — смещение имени в байтах (или -1);
      alpn            — список предложенных протоколов;
      supported_versions — версии из соответствующего расширения;
      extensions      — номера всех расширений по порядку.

    Версий здесь три разных места, и это не избыточность: в новых версиях
    протокола первые две намеренно врут ради совместимости с посредниками,
    а настоящий список версий лежит в отдельном расширении.
    """
    if len(data) < 45:
        raise WireError("слишком мало байт для приветствия: %d" % len(data))
    if data[0] != 0x16:
        raise WireError("это не запись рукопожатия: первый байт 0x%02x" % data[0])

    i = 1
    record_version, i = _u16(data, i)
    record_len, i = _u16(data, i)

    handshake_type, i = _u8(data, i)
    if handshake_type != 0x01:
        raise WireError("это не приветствие клиента: тип 0x%02x" % handshake_type)
    _, i = _u24(data, i)

    hello_version, i = _u16(data, i)
    i += 32                                    # случайное поле

    session_len, i = _u8(data, i)
    i += session_len

    suites_len, i = _u16(data, i)
    i += suites_len

    comp_len, i = _u8(data, i)
    i += comp_len

    result = {
        "record_version": record_version,
        "hello_version": hello_version,
        "server_name": "",
        "server_name_at": -1,
        "alpn": [],
        "supported_versions": [],
        "extensions": [],
    }

    if i + 2 > len(data):
        return result

    ext_total, i = _u16(data, i)
    end = min(i + ext_total, len(data))

    while i + 4 <= end:
        ext_type, i = _u16(data, i)
        ext_len, i = _u16(data, i)
        body_at = i
        body = data[body_at:body_at + ext_len]
        i += ext_len
        result["extensions"].append(ext_type)

        if ext_type == 0x0000 and len(body) >= 5:
            # Имя сервера. Смещение считается ОТ НАЧАЛА ВСЕХ БАЙТОВ, а не
            # от начала расширения: проверка должна уметь вернуться к
            # этому месту в исходном дампе.
            name_len = (body[3] << 8) | body[4]
            at = body_at + 5
            result["server_name"] = data[at:at + name_len].decode(
                "ascii", "replace")
            result["server_name_at"] = at
        elif ext_type == 0x0010 and len(body) >= 2:
            j = 2
            while j < len(body):
                proto_len = body[j]
                j += 1
                result["alpn"].append(
                    body[j:j + proto_len].decode("ascii", "replace"))
                j += proto_len
        elif ext_type == 0x002b and len(body) >= 1:
            j = 1
            while j + 1 < len(body) + 1 and j + 2 <= len(body) + 1:
                if j + 2 > len(body):
                    break
                result["supported_versions"].append((body[j] << 8) | body[j + 1])
                j += 2

    return result


def name_at_offset(data, offset, length):
    """Что реально лежит в байтах по этому смещению.

    Существует ровно затем, чтобы проверка не верила разбору на слово.
    """
    return data[offset:offset + length].decode("ascii", "replace")


VERSION_NAMES = {
    0x0301: "TLS 1.0",
    0x0302: "TLS 1.1",
    0x0303: "TLS 1.2",
    0x0304: "TLS 1.3",
}


def version_name(code):
    return VERSION_NAMES.get(code, "0x%04x" % code)


# Метка о понижении версии.
#
# Сервер, который УМЕЕТ 1.3, но договорился на 1.2, обязан положить эти
# восемь байт в конец случайного поля своего приветствия. Клиент,
# умеющий 1.3, увидит их и поймёт, что понижение могло быть навязано
# посредником, а не выбрано честно.
#
# Это байты, а не рассуждение: их наличие проверяется, а не постулируется.
DOWNGRADE_TO_12 = bytes.fromhex("444f574e47524401")
DOWNGRADE_TO_11_OR_BELOW = bytes.fromhex("444f574e47524400")


def parse_server_hello(data):
    """Разбирает приветствие сервера и достаёт случайное поле.

    Возвращает согласованную версию, случайное поле, его смещение и то,
    оканчивается ли оно меткой о понижении.
    """
    if len(data) < 45:
        raise WireError("слишком мало байт для приветствия сервера: %d"
                        % len(data))
    if data[0] != 0x16:
        raise WireError("это не запись рукопожатия: первый байт 0x%02x"
                        % data[0])

    i = 1
    record_version, i = _u16(data, i)
    _, i = _u16(data, i)

    handshake_type, i = _u8(data, i)
    if handshake_type != 0x02:
        raise WireError("это не приветствие сервера: тип 0x%02x"
                        % handshake_type)
    _, i = _u24(data, i)

    hello_version, i = _u16(data, i)
    random_at = i
    random = data[i:i + 32]
    i += 32

    # Дальше — согласованная версия. В новой версии протокола поле версии
    # в самом приветствии ВСЕГДА говорит 1.2, а настоящая версия лежит в
    # отдельном расширении. Без этого разбора таблица показывала бы 1.2 в
    # обеих строках пары о понижении, и вывод «оба договорились на
    # старую» был бы неверным.
    negotiated = hello_version
    session_len, i = _u8(data, i)
    i += session_len
    i += 2                                     # выбранный набор шифров
    i += 1                                     # способ сжатия
    if i + 2 <= len(data):
        ext_total, i = _u16(data, i)
        end = min(i + ext_total, len(data))
        while i + 4 <= end:
            ext_type, i = _u16(data, i)
            ext_len, i = _u16(data, i)
            if ext_type == 0x002b and ext_len >= 2:
                negotiated = (data[i] << 8) | data[i + 1]
            i += ext_len

    tail = random[-8:]
    return {
        "negotiated_version": negotiated,
        "record_version": record_version,
        "hello_version": hello_version,
        "random_at": random_at,
        "random": random.hex(),
        "downgrade_tail": tail.hex(),
        "downgrade_to_12": tail == DOWNGRADE_TO_12,
        "downgrade_to_11_or_below": tail == DOWNGRADE_TO_11_OR_BELOW,
    }


# Типы записей и сообщений рукопожатия — ровно те, что нужны стенду.
RECORD_NAMES = {20: "смена шифра", 21: "тревога", 22: "рукопожатие",
                23: "прикладные данные"}
HANDSHAKE_NAMES = {1: "ClientHello", 2: "ServerHello", 11: "Certificate",
                   12: "ServerKeyExchange", 14: "ServerHelloDone"}


def parse_records(data):
    """Проходит по записям и вынимает сообщения рукопожатия ОТКРЫТЫМ текстом.

    Нужно, чтобы отличить видимое от невидимого. В старой версии протокола
    сертификат сервера едет открытым текстом и читается любым, кто смотрит
    на провод; в новой он уезжает внутрь зашифрованной записи, и снаружи
    видно лишь её длину.

    Разбор идёт по структуре, а не поиском подстроки: зашифрованная запись
    тоже может случайно содержать что угодно.
    """
    records = []
    i = 0
    while i + 5 <= len(data):
        ctype = data[i]
        length = (data[i + 3] << 8) | data[i + 4]
        body = data[i + 5:i + 5 + length]
        record = {
            "type": ctype,
            "type_name": RECORD_NAMES.get(ctype, "тип %d" % ctype),
            "length": length,
            "handshake": [],
        }
        if ctype == 22:
            j = 0
            while j + 4 <= len(body):
                htype = body[j]
                hlen = (body[j + 1] << 16) | (body[j + 2] << 8) | body[j + 3]
                if j + 4 + hlen > len(body):
                    break
                record["handshake"].append({
                    "type": htype,
                    "name": HANDSHAKE_NAMES.get(htype, "сообщение %d" % htype),
                    "length": hlen,
                })
                j += 4 + hlen
        records.append(record)
        i += 5 + length
    return records


def plaintext_certificate(records):
    """Длина сертификата, читаемого прямо с провода, или 0.

    Ноль здесь означает не «сертификата нет», а «снаружи его не прочитать».
    """
    for record in records:
        for message in record["handshake"]:
            if message["type"] == 11:
                return message["length"]
    return 0
