package vdb;

import io.milvus.v2.client.ConnectConfig;
import io.milvus.v2.client.MilvusClientV2;
import io.milvus.v2.service.vector.request.SearchReq;
import io.milvus.v2.service.vector.request.data.FloatVec;
import io.qdrant.client.QdrantClient;
import io.qdrant.client.QdrantGrpcClient;
import io.qdrant.client.grpc.Points.QueryPoints;
import io.qdrant.client.grpc.Points.SearchParams;
import io.weaviate.client.Config;
import io.weaviate.client.WeaviateClient;
import io.weaviate.client.v1.graphql.query.argument.NearVectorArgument;
import io.weaviate.client.v1.graphql.query.fields.Field;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;

import static io.qdrant.client.QueryFactory.nearest;

/**
 * Выдача первых 200 запросов истины официальным Java-клиентом движка — те же
 * запросы, что `ebench dump`.
 *
 * <pre>java -cp … vdb.Dump qdrant|milvus|weaviate /data/quora 40</pre>
 */
public final class Dump {
    static final int DIM = 384, K = 10;

    public static void main(String[] args) throws Exception {
        String engine = args[0];
        Path root = Path.of(args[1]);
        int ef = Integer.parseInt(args[2]);
        var qb = ByteBuffer.wrap(Files.readAllBytes(root.resolve("queries.f32"))).order(ByteOrder.LITTLE_ENDIAN).asFloatBuffer();
        List<Integer> rows = Files.readAllLines(root.resolve("gt.rows")).stream().map(String::trim).map(Integer::parseInt).toList();
        List<float[]> queries = new ArrayList<>();
        for (int r : rows.subList(0, 200)) {
            float[] v = new float[DIM];
            qb.get(r * DIM, v);
            queries.add(v);
        }
        List<List<Long>> out = switch (engine) {
            case "qdrant" -> qdrant(queries, ef);
            case "milvus" -> milvus(queries, ef);
            case "weaviate" -> weaviate(queries);
            default -> throw new IllegalArgumentException(engine);
        };
        System.out.println("{\"cmd\": \"dump\", \"engine\": \"" + engine + "\", \"ef\": " + ef + ", \"ids\": ["
                + out.stream().map(l -> l.stream().map(String::valueOf).collect(Collectors.joining(", ", "[", "]")))
                        .collect(Collectors.joining(", ")) + "]}");
        System.exit(0);
    }

    static List<List<Long>> qdrant(List<float[]> queries, int ef) throws Exception {
        try (var c = new QdrantClient(QdrantGrpcClient.newBuilder("qdrant", 6334, false).build())) {
            var out = new ArrayList<List<Long>>();
            for (float[] q : queries) {
                var res = c.queryAsync(QueryPoints.newBuilder().setCollectionName("docs").setQuery(nearest(q))
                        .setLimit(K).setParams(SearchParams.newBuilder().setHnswEf(ef)).build()).get();
                out.add(res.stream().map(p -> p.getId().getNum()).toList());
            }
            return out;
        }
    }

    static List<List<Long>> milvus(List<float[]> queries, int ef) {
        var c = new MilvusClientV2(ConnectConfig.builder().uri("http://milvus:19530").build());
        try {
            var out = new ArrayList<List<Long>>();
            for (float[] q : queries) {
                var resp = c.search(SearchReq.builder().collectionName("docs").annsField("emb")
                        .data(List.of(new FloatVec(q))).topK(K).searchParams(Map.of("ef", ef)).build());
                out.add(resp.getSearchResults().get(0).stream().map(r -> ((Number) r.getId()).longValue()).toList());
            }
            return out;
        } finally {
            c.close();
        }
    }

    /** ef для Weaviate — свойство индекса в схеме, его выставляет ebench перед выгрузкой. */
    @SuppressWarnings("unchecked")
    static List<List<Long>> weaviate(List<float[]> queries) {
        var c = new WeaviateClient(new Config("http", "weaviate:8080"));
        var out = new ArrayList<List<Long>>();
        for (float[] q : queries) {
            Float[] boxed = new Float[q.length];
            for (int i = 0; i < q.length; i++) boxed[i] = q[i];
            var res = c.graphQL().get().withClassName("Doc").withFields(Field.builder().name("rid").build())
                    .withNearVector(NearVectorArgument.builder().vector(boxed).build()).withLimit(K).run();
            if (res.hasErrors()) throw new IllegalStateException(res.getError().toString());
            var data = (Map<String, Object>) res.getResult().getData();
            var items = (List<Map<String, Object>>) ((Map<String, Object>) data.get("Get")).get("Doc");
            out.add(items.stream().map(m -> ((Number) m.get("rid")).longValue()).toList());
        }
        return out;
    }
}
