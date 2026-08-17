package tech.khorost.mongodb.ops;

import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import com.mongodb.client.MongoCollection;
import com.mongodb.client.MongoCursor;
import com.mongodb.client.MongoDatabase;
import com.mongodb.client.model.Filters;
import com.mongodb.client.model.Updates;
import com.mongodb.client.model.changestream.ChangeStreamDocument;
import com.mongodb.client.model.changestream.FullDocument;
import com.mongodb.client.model.changestream.OperationType;
import org.bson.BsonDocument;
import org.bson.Document;

import java.time.Duration;
import java.time.Instant;

/**
 * Java-зеркало change streams-части ../../ops-stand (Go) — стенд #7 серии
 * "MongoDB: глубокое погружение": {@code watch()} на выделенной коллекции,
 * серия insert/update/delete, и ГЛАВНОЕ доказательство {@code resumeAfter}:
 * консьюмер закрывается СРАЗУ после insert-события, update происходит ПОКА
 * консьюмер закрыт (имитация простоя потребителя), переоткрытие change
 * stream с {@code resumeAfter(token)} должно вернуть ИМЕННО это пропущенное
 * update-событие первым — ни потери, ни повторной доставки уже виденного
 * insert. ТОТ ЖЕ сценарий, что и Go-стенд, на СВОЕЙ коллекции
 * ({@code cs_demo_java}), чтобы не пересекаться с Go-прогоном на том же
 * живом кластере — подтверждает, что поведение resumeAfter не артефакт
 * конкретного драйвера, а свойство сервера.
 *
 * <p>Пул соединений, retryable writes и backup/restore — только в Go-стенде
 * (см. package-doc ../../ops-stand/main.go); бриф Task 9 требует Java-
 * зеркало ИМЕННО для change streams, не полное дублирование всех
 * сценариев.
 */
public final class Main {

    private static final String DB_NAME = "cookbook";
    private static final String CS_COLL = "cs_demo_java";
    private static final String DOC_ID = "cs-demo-doc-java";

    private Main() {
    }

    public static void main(String[] args) {
        String mongoUri = mustEnv("MONGO_URI");
        try (MongoClient client = MongoClients.create(mongoUri)) {
            MongoDatabase db = client.getDatabase(DB_NAME);
            MongoCollection<Document> coll = db.getCollection(CS_COLL);
            coll.drop();

            changeStreamScenario(coll);
        }
        log("готово.");
    }

    private static void changeStreamScenario(MongoCollection<Document> coll) {
        MongoCursor<ChangeStreamDocument<Document>> cursor1 = coll.watch()
                .fullDocument(FullDocument.UPDATE_LOOKUP)
                .iterator();

        Instant t0 = Instant.now();
        coll.insertOne(new Document("_id", DOC_ID).append("marker", "cs-demo-java").append("seq", 1));

        ChangeStreamDocument<Document> insertEvt = cursor1.next();
        Duration insertLatency = Duration.between(t0, Instant.now());
        OperationType insertOpType = insertEvt.getOperationType();
        BsonDocument token1 = insertEvt.getResumeToken();

        log("FIXTURE ops-java: cs_insert_op_type=%s cs_insert_latency=%s cs_insert_resume_token_present=%s"
                .formatted(insertOpType, insertLatency, token1 != null));
        if (insertOpType != OperationType.INSERT) {
            throw new IllegalStateException("assert: первое событие change stream должно быть INSERT, получено " + insertOpType);
        }
        if (token1 == null) {
            throw new IllegalStateException("assert: resume token после insert-события отсутствует");
        }

        // Закрываем консьюмера — имитация простоя потребителя (ДО открытия
        // следующего change stream, намеренно, не в try-with-resources).
        cursor1.close();

        // Событие ПОКА консьюмер закрыт.
        coll.updateOne(Filters.eq("_id", DOC_ID), Updates.set("seq", 2));

        try (MongoCursor<ChangeStreamDocument<Document>> cursor2 = coll.watch()
                .fullDocument(FullDocument.UPDATE_LOOKUP)
                .resumeAfter(token1)
                .iterator()) {

            Instant t1 = Instant.now();
            ChangeStreamDocument<Document> resumedEvt = cursor2.next();
            Duration resumeLatency = Duration.between(t1, Instant.now());
            OperationType resumedOpType = resumedEvt.getOperationType();

            log("FIXTURE ops-java: cs_resume_first_event_op_type=%s cs_resume_latency=%s"
                    .formatted(resumedOpType, resumeLatency));
            if (resumedOpType != OperationType.UPDATE) {
                throw new IllegalStateException(
                        "assert: ПЕРВОЕ событие после resumeAfter должно быть UPDATE (пропущенное во время простоя), получено " + resumedOpType);
            }
            log("assert OK: resumeAfter вернул именно пропущенное во время простоя update-событие первым (" + resumeLatency + ") — ни потери, ни дублирования insert");

            Instant t2 = Instant.now();
            coll.deleteOne(Filters.eq("_id", DOC_ID));
            ChangeStreamDocument<Document> deleteEvt = cursor2.next();
            Duration deleteLatency = Duration.between(t2, Instant.now());
            OperationType deleteOpType = deleteEvt.getOperationType();

            log("FIXTURE ops-java: cs_delete_op_type=%s cs_delete_latency=%s".formatted(deleteOpType, deleteLatency));
            if (deleteOpType != OperationType.DELETE) {
                throw new IllegalStateException("assert: третье событие должно быть DELETE, получено " + deleteOpType);
            }
            log("assert OK: change stream (Java) доставил insert/update/delete в правильном порядке (insert=%s resume-update=%s delete=%s)"
                    .formatted(insertLatency, resumeLatency, deleteLatency));
        }
    }

    private static String mustEnv(String key) {
        String v = System.getenv(key);
        if (v == null || v.isEmpty()) {
            throw new IllegalStateException("обязательная переменная окружения " + key + " не задана");
        }
        return v;
    }

    private static void log(String msg) {
        System.out.println("[" + Instant.now() + "] " + msg);
    }
}
