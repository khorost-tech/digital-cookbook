import com.google.common.hash.BloomFilter;
import com.google.common.hash.Funnels;
import com.clearspring.analytics.stream.cardinality.HyperLogLogPlus;
import com.clearspring.analytics.stream.frequency.CountMinSketch;

import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.util.Locale;

public class Main {
    public static void main(String[] args) throws Exception {
        // Вывод не должен зависеть от локали/кодировки ОС (Windows-консоль по умолчанию не UTF-8).
        System.setOut(new PrintStream(System.out, true, StandardCharsets.UTF_8));

        String exp = args.length > 0 ? args[0] : "all";
        if (exp.equals("bloom") || exp.equals("all")) bloom();
        if (exp.equals("hll") || exp.equals("all")) hll();
        if (exp.equals("countmin") || exp.equals("all")) countmin();
    }

    static void bloom() {
        int n = 1_000_000, queries = 1_000_000;
        double fpp = 0.01;
        BloomFilter<String> bf =
            BloomFilter.create(Funnels.stringFunnel(StandardCharsets.UTF_8), n, fpp);
        for (int i = 0; i < n; i++) bf.put("present-" + i);
        int fp = 0;
        for (int i = 0; i < queries; i++) if (bf.mightContain("absent-" + i)) fp++;
        System.out.printf(Locale.ROOT, "Guava Bloom: target=%.4f  expectedFpp=%.4f  actual=%.4f%n",
            fpp, bf.expectedFpp(), (double) fp / queries);
    }

    static void hll() throws Exception {
        int n = 1_000_000;
        HyperLogLogPlus h = new HyperLogLogPlus(14, 25);
        for (int i = 0; i < n; i++) h.offer("u-" + i);
        long est = h.cardinality();
        System.out.printf(Locale.ROOT, "stream-lib HLL++: true=%d est=%d err=%.3f%%%n",
            n, est, (est - (double) n) / n * 100);
    }

    static void countmin() {
        // epsOfTotalCount, confidence, seed
        CountMinSketch cms = new CountMinSketch(0.0001, 0.99, 42);
        for (int i = 0; i < 200_000; i++) cms.add("k-" + (i % 1000), 1);
        cms.add("HOT", 80_000);
        final long truthHot = 80_000;
        long est = cms.estimateCount("HOT");
        // HOT доминирует над шумом, поэтому его переоценка округляется до 0.00% —
        // полный тренд падения переоценки с ростом width/depth см. в Go-стенде
        // (probabilistic/go — эксперимент countmin по шумовым ключам).
        System.out.printf(Locale.ROOT, "stream-lib Count-Min: HOT truth=%d est=%d переоценка=%.2f%%%n",
            truthHot, est, (est - (double) truthHot) / truthHot * 100);
    }
}
