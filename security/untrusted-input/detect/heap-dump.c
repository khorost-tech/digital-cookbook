/* Что именно лежит за нашим блоком и что портит каждый байт OOB-записи. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <malloc.h>
#include <stdint.h>

int main(int argc, char **argv)
{
    int overflow = (argc > 1) ? atoi(argv[1]) : 0;

    volatile char *buf = (volatile char *)malloc(32);
    size_t usable = malloc_usable_size((void *)buf);

    printf("запрошено:        32 байта\n");
    printf("usable_size:      %zu байт  (индексы p[0]..p[%zu])\n", usable, usable - 1);
    printf("OOB-запись:       %d байт -> индексы p[32]..p[%d]\n", overflow, 32 + overflow - 1);
    printf("выход за usable:  %s\n",
           (32 + overflow > (int)usable) ? "ДА" : "нет");

    /* Заголовок следующего чанка лежит сразу за usable-областью.
       На x86-64 это 8 байт поля size (prev_size соседа переиспользуется
       под данные текущего чанка, когда тот занят). */
    volatile uint64_t *next_size = (volatile uint64_t *)((char *)buf + usable);
    uint64_t before = *next_size;

    for (int i = 0; i < 32; i++)
        buf[i] = 0x41;
    for (int i = 32; i < 32 + overflow; i++)
        buf[i] = 0x42;

    uint64_t after = *next_size;

    /*
     * Поле size хранит размер вместе со служебными битами (младшие три):
     * PREV_INUSE, IS_MMAPPED, NON_MAIN_ARENA. Чтобы получить собственно размер
     * чанка, их надо замаскировать — иначе значение читается неверно, и вывод
     * «размер стал таким-то» будет ошибочным.
     */
    const uint64_t SIZE_BITS = 0x7;
    uint64_t size_before = before & ~SIZE_BITS;
    uint64_t size_after = after & ~SIZE_BITS;

    printf("поле size соседа: было 0x%016lx, стало 0x%016lx  %s\n",
           before, after, (before != after) ? "<- ИЗМЕНЕНО" : "(не тронуто)");
    printf("  chunksize без флагов: было %lu, стало %lu\n", size_before, size_after);
    printf("  флаги PREV_INUSE/IS_MMAPPED/NON_MAIN_ARENA: было %lu/%lu/%lu, стало %lu/%lu/%lu\n",
           before & 1, (before >> 1) & 1, (before >> 2) & 1,
           after & 1, (after >> 1) & 1, (after >> 2) & 1);

    /* Не освобождаем: цель — только посмотреть на заголовок. */
    return 0;
}
