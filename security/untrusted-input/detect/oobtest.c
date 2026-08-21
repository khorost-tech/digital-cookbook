/*
 * Учебная программа для замера детекта повреждения кучи.
 *
 * ЧТО ЭТО. Собственный контролируемый баг: выделяем блок и пишем N байт за его
 * границей. Тот же класс, что в CVE-2026-8461 (PixelSmash), где декодер писал
 * на строку дальше выделенного буфера. Это НЕ эксплойт и не воспроизведение
 * какой-либо уязвимости: здесь нет ни подготовки кучи, ни перехвата указателей,
 * ни полезной нагрузки — только запись мусора за границей своего же блока,
 * чтобы посмотреть, заметит ли её механизм защиты.
 *
 * ЗАЧЕМ volatile. Запись за границей — неопределённое поведение, и компилятор
 * вправе её выбросить. Проверено на этом стенде: без volatile при -O2 даже
 * AddressSanitizer «ничего не находит» — потому что находить уже нечего.
 * volatile заставляет компилятор выполнить каждую запись буквально.
 *
 * Сборка: см. build.sh
 * Использование:
 *   ./oobtest <смещение>        — записать <смещение> байт за границей
 *   ./oobtest <смещение> verify — то же + проверить, что запись реально прошла
 *   ./oobtest bench <итераций>  — нагрузка без OOB, для замера накладных расходов
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define BLOCK 32
#define FILL_IN 0x41  /* 'A' — внутри блока */
#define FILL_OOB 0x42 /* 'B' — за границей  */

/* Печать целого без malloc и stdio: только write(2). */
static void write_int(int fd, int value)
{
    char digits[16];
    int n = 0;
    if (value == 0) {
        digits[n++] = '0';
    } else {
        while (value > 0 && n < (int)sizeof(digits)) {
            digits[n++] = (char)('0' + value % 10);
            value /= 10;
        }
    }
    char out[16];
    for (int i = 0; i < n; i++)
        out[i] = digits[n - 1 - i];
    ssize_t ignored = write(fd, out, (size_t)n);
    (void)ignored;
}

/* Результат контроля, выводимый без единой аллокации. */
static void write_verify_result(int visible, int overflow)
{
    const char *head = "verify: oob_write_visible=";
    const char *val = visible ? "yes" : "no";
    const char *mid = " bytes=";
    ssize_t ignored;
    ignored = write(1, head, strlen(head));
    ignored = write(1, val, strlen(val));
    ignored = write(1, mid, strlen(mid));
    (void)ignored;
    write_int(1, overflow);
    ignored = write(1, "\n", 1);
    (void)ignored;
}

/*
 * Пишет overflow байт за границей блока размером BLOCK.
 * Возвращает 0, если после записи данные читаются обратно (контроль того,
 * что запись действительно произошла), иначе 1.
 */
static int do_overflow(int overflow, int verify)
{
    volatile char *buf = (volatile char *)malloc(BLOCK);
    if (!buf) {
        fprintf(stderr, "malloc failed\n");
        return 2;
    }

    for (int i = 0; i < BLOCK; i++)
        buf[i] = FILL_IN;

    /* Собственно OOB-запись. */
    for (int i = BLOCK; i < BLOCK + overflow; i++)
        buf[i] = FILL_OOB;

    int written_back = 1;
    if (verify) {
        /*
         * КОНТРОЛЬ МЕТОДА. Без него вывод «механизм не поймал» ничего не стоит:
         * может, записи просто не было. Читаем записанное обратно ДО free(),
         * пока никто не успел переиспользовать эту память.
         */
        written_back = 1;
        for (int i = BLOCK; i < BLOCK + overflow; i++) {
            if (buf[i] != FILL_OOB) {
                written_back = 0;
                break;
            }
        }
        /*
         * Вывод контроля идёт через write(2), а НЕ через printf.
         *
         * printf работает через буферизованный поток и может сам вызвать
         * malloc — на уже повреждённой куче это падает раньше, чем сообщение
         * дойдёт до наблюдателя. Проверено на этом стенде: с printf контроль
         * терялся на всех смещениях от 16 байт, и выглядело это так, будто
         * запись не происходила. write(2) идёт напрямую в дескриптор, без
         * аллокаций и буферизации.
         *
         * Именно контроль важнее всего сохранить: без него вывод «механизм
         * не поймал» не значит ничего.
         */
        write_verify_result(written_back, overflow);
    }

    free((void *)buf);

    /*
     * Вторая аллокация и освобождение. Именно здесь glibc обычно спотыкается о
     * повреждённые служебные структуры — если повреждение вообще их задело.
     */
    volatile char *other = (volatile char *)malloc(64);
    if (!other) {
        fprintf(stderr, "malloc failed\n");
        return 2;
    }
    for (int i = 0; i < 64; i++)
        other[i] = FILL_IN;
    free((void *)other);

    return written_back ? 0 : 1;
}

/* Нагрузка без OOB: нужна для замера накладных расходов механизмов. */
static void do_bench(long iterations)
{
    for (long i = 0; i < iterations; i++) {
        size_t sz = 16 + (size_t)(i % 512);
        volatile char *p = (volatile char *)malloc(sz);
        if (!p)
            continue;
        for (size_t j = 0; j < sz; j += 64)
            p[j] = FILL_IN;
        free((void *)p);
    }
}

int main(int argc, char **argv)
{
    if (argc < 2) {
        fprintf(stderr, "usage: %s <overflow-bytes> [verify] | bench <iterations>\n", argv[0]);
        return 2;
    }

    if (strcmp(argv[1], "bench") == 0) {
        long iterations = (argc > 2) ? atol(argv[2]) : 1000000;
        do_bench(iterations);
        printf("finished normally\n");
        return 0;
    }

    int overflow = atoi(argv[1]);
    int verify = (argc > 2 && strcmp(argv[2], "verify") == 0);

    int rc = do_overflow(overflow, verify);
    if (rc == 2)
        return 2;

    /*
     * Этот маркер — главный сигнал эксперимента. Если он напечатан и код
     * возврата нулевой, значит механизм защиты повреждение НЕ заметил:
     * процесс отработал штатно, в логах пусто, куча при этом испорчена.
     */
    printf("finished normally\n");
    return 0;
}
