package tech.khorost.mm;

import java.util.concurrent.ThreadLocalRandom;

/**
 * Опора 3 — паузы GC, ОДИН прогон, качественно.
 *
 * Аллокационно-тяжёлый ворклоад: непрерывно создаём короткоживущие массивы
 * и небольшую долю удерживаем в кольцевом буфере (порождает промоушен в
 * старое поколение / нагрузку на конкурентный сборщик). Один и тот же код
 * прогоняется дважды снаружи — под -XX:+UseG1GC и под -XX:+UseZGC — а паузы
 * читаются из -Xlog:gc. Это ЗАМЕР ОДНОГО ПРОГОНА, не свойство языка.
 */
public final class GcWorkload {

    public static void main(String[] args) {
        final int iterations = 12_000_000;
        final int liveWindow = 100_000;
        Object[] live = new Object[liveWindow];

        long checksum = 0;
        for (int i = 0; i < iterations; i++) {
            int sz = 16 + ThreadLocalRandom.current().nextInt(240);
            byte[] garbage = new byte[sz];
            garbage[0] = (byte) i;
            garbage[sz - 1] = (byte) (i >>> 8);
            checksum += garbage[0] + garbage[sz - 1];

            // Удерживаем часть аллокаций -> нагрузка на промоушен/конкурентный GC.
            live[i % liveWindow] = garbage;
        }

        // Мешаем оптимизатору выкинуть аллокации.
        long acc = 0;
        for (Object o : live) {
            if (o != null) {
                acc += ((byte[]) o).length;
            }
        }
        System.out.println("checksum=" + checksum + " live_bytes_sample=" + acc);
    }
}
