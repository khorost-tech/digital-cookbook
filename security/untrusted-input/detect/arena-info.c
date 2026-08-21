/*
 * Размер арены и top chunk — чтобы проверить (а не предположить) объяснение
 * порога детекта.
 *
 * Ошибка, которую выдаёт glibc при OOB на 10 байт, — "malloc(): corrupted top
 * size". В исходниках glibc 2.39 (malloc/malloc.c, _int_malloc) ей соответствует
 * проверка:
 *
 *     victim = av->top;
 *     size = chunksize (victim);
 *     if (__glibc_unlikely (size > av->system_mem))
 *         malloc_printerr ("malloc(): corrupted top size");
 *
 * Напрашивается объяснение «повреждённый размер превысил объём арены». Эта
 * программа существует ровно для того, чтобы его проверить, — и оно НЕ
 * подтверждается: см. README, раздел про границы объяснения.
 */

#include <stdio.h>
#include <stdlib.h>
#include <malloc.h>

int main(void)
{
    struct mallinfo2 mi = mallinfo2();
    printf("до аллокаций:      arena=%zu, top(keepcost)=%zu\n", mi.arena, mi.keepcost);

    void *p = malloc(32);
    mi = mallinfo2();
    printf("после malloc(32):  arena=%zu, top(keepcost)=%zu\n", mi.arena, mi.keepcost);

    printf("\nНаблюдаемые в dump-layout значения chunksize соседа:\n");
    printf("  исходный:      4112  -> %s arena\n", 4112  > mi.arena ? "больше" : "меньше");
    printf("  при OOB 9  Б:  4160  -> %s arena\n", 4160  > mi.arena ? "больше" : "меньше");
    printf("  при OOB 10 Б: 16960  -> %s arena\n", 16960 > mi.arena ? "больше" : "меньше");
    printf("\nВывод: оба значения меньше arena, и исходный chunksize соседа (4112)\n");
    printf("не совпадает с размером top chunk. Значит объяснение «размер превысил\n");
    printf("арену» простым сравнением НЕ подтверждается — точный путь, которым\n");
    printf("повреждение доходит до проверки top size, этот стенд не устанавливает.\n");

    free(p);
    return 0;
}
