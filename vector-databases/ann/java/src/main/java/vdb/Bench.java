package vdb;

import io.github.jbellis.jvector.graph.GraphIndexBuilder;
import io.github.jbellis.jvector.graph.GraphSearcher;
import io.github.jbellis.jvector.graph.ListRandomAccessVectorValues;
import io.github.jbellis.jvector.graph.similarity.BuildScoreProvider;
import io.github.jbellis.jvector.graph.similarity.DefaultSearchScoreProvider;
import io.github.jbellis.jvector.util.Bits;
import io.github.jbellis.jvector.vector.VectorSimilarityFunction;
import io.github.jbellis.jvector.vector.VectorizationProvider;
import io.github.jbellis.jvector.vector.types.VectorFloat;

import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;

/**
 * Вторая, независимая реализация графового индекса — jvector (граф в духе
 * DiskANN/Vamana с иерархией, как в HNSW) на тех же векторах и той же истине.
 *
 * <pre>java -cp … vdb.Bench /data/quora M beamWidth ef1,ef2,…</pre>
 *
 * Печатает одну строку JSON: recall@10 и число посещённых узлов на запрос для
 * каждого rerankK (ширина обхода — аналог ef).
 */
public final class Bench {
    static final int DIM = 384, K = 10, GT_K = 100;

    public static void main(String[] args) throws IOException {
        Path root = Path.of(args[0]);
        int m = Integer.parseInt(args[1]), beam = Integer.parseInt(args[2]);
        int[] efs = Arrays.stream(args[3].split(",")).mapToInt(Integer::parseInt).toArray();

        var vts = VectorizationProvider.getInstance().getVectorTypeSupport();
        List<VectorFloat<?>> corpus = read(root.resolve("corpus.f32"), vts);
        List<VectorFloat<?>> queries = read(root.resolve("queries.f32"), vts);
        List<Integer> rows = Files.readAllLines(root.resolve("gt.rows")).stream().map(String::trim).map(Integer::parseInt).toList();
        ByteBuffer gt = ByteBuffer.wrap(Files.readAllBytes(root.resolve("gt.i32"))).order(ByteOrder.LITTLE_ENDIAN);

        var ravv = new ListRandomAccessVectorValues(corpus, DIM);
        var sim = VectorSimilarityFunction.DOT_PRODUCT;
        long t0 = System.nanoTime();
        var builder = new GraphIndexBuilder(BuildScoreProvider.randomAccessScoreProvider(ravv, sim), DIM, m, beam, 1.2f, 1.2f, true);
        var graph = builder.build(ravv);
        double buildS = (System.nanoTime() - t0) / 1e9;

        var sb = new StringBuilder();
        sb.append(String.format(Locale.ROOT, "{\"algo\": \"jvector\", \"n\": %d, \"m\": %d, \"beam\": %d, \"build_s\": %.1f, \"points\": [",
                corpus.size(), m, beam, buildS));
        try (var searcher = new GraphSearcher(graph)) {
            for (int e = 0; e < efs.length; e++) {
                int ef = efs[e];
                double recall = 0, visited = 0;
                long[] lat = new long[rows.size()];
                for (int i = 0; i < rows.size(); i++) {
                    var q = queries.get(rows.get(i));
                    var ssp = DefaultSearchScoreProvider.exact(q, sim, ravv);
                    long t = System.nanoTime();
                    var res = searcher.search(ssp, K, ef, 0f, 0f, Bits.ALL);
                    lat[i] = (System.nanoTime() - t) / 1000;
                    visited += res.getVisitedCount();
                    var truth = new HashSet<Integer>();
                    for (int j = 0; j < K; j++) truth.add(gt.getInt(4 * (i * GT_K + j)));
                    int hit = 0;
                    for (var ns : res.getNodes()) if (truth.contains(ns.node)) hit++;
                    recall += hit / (double) K;
                }
                Arrays.sort(lat);
                int n = rows.size();
                sb.append(String.format(Locale.ROOT, "%s{\"param\": %d, \"recall_at_10\": %.4f, \"visited_per_query\": %.1f, \"p50_us\": %d}",
                        e == 0 ? "" : ", ", ef, recall / n, visited / n, lat[n / 2]));
            }
        }
        sb.append("]}");
        System.out.println(sb);
    }

    static List<VectorFloat<?>> read(Path p, io.github.jbellis.jvector.vector.types.VectorTypeSupport vts) throws IOException {
        var b = ByteBuffer.wrap(Files.readAllBytes(p)).order(ByteOrder.LITTLE_ENDIAN).asFloatBuffer();
        int n = b.remaining() / DIM;
        var out = new ArrayList<VectorFloat<?>>(n);
        for (int i = 0; i < n; i++) {
            float[] v = new float[DIM];
            b.get(v);
            out.add(vts.createFloatVector(v));
        }
        return out;
    }
}
