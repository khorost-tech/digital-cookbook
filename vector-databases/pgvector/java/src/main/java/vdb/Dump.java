package vdb;

import com.pgvector.PGvector;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.file.Files;
import java.nio.file.Path;
import java.sql.DriverManager;
import java.util.ArrayList;
import java.util.List;
import java.util.stream.Collectors;

/**
 * Те же запросы, что `vbench dump`, через JDBC и pgvector-java: выдача первых
 * 200 запросов истины при hnsw.ef_search = ef.
 *
 * <pre>java -cp … vdb.Dump /data/quora jdbc:postgresql://pg:5432/vdb 40</pre>
 */
public final class Dump {
    static final int DIM = 384;

    public static void main(String[] args) throws Exception {
        Path root = Path.of(args[0]);
        int ef = Integer.parseInt(args[2]);
        var q = ByteBuffer.wrap(Files.readAllBytes(root.resolve("queries.f32"))).order(ByteOrder.LITTLE_ENDIAN).asFloatBuffer();
        List<Integer> rows = Files.readAllLines(root.resolve("gt.rows")).stream().map(String::trim).map(Integer::parseInt).toList();
        var out = new ArrayList<String>();
        try (var conn = DriverManager.getConnection(args[1], "postgres", "pg")) {
            PGvector.addVectorType(conn);
            try (var st = conn.createStatement()) {
                st.execute("SET hnsw.ef_search = " + ef);
            }
            try (var ps = conn.prepareStatement("SELECT id FROM docs ORDER BY embedding <#> ? LIMIT 10")) {
                for (int r : rows.subList(0, 200)) {
                    float[] v = new float[DIM];
                    q.get(r * DIM, v);
                    ps.setObject(1, new PGvector(v));
                    var ids = new ArrayList<Integer>();
                    try (var rs = ps.executeQuery()) {
                        while (rs.next()) ids.add(rs.getInt(1));
                    }
                    out.add(ids.stream().map(String::valueOf).collect(Collectors.joining(", ", "[", "]")));
                }
            }
        }
        System.out.println("{\"cmd\": \"dump\", \"ef_search\": " + ef + ", \"ids\": [" + String.join(", ", out) + "]}");
    }
}
