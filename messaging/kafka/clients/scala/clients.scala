//> using scala 3.3
//> using dep com.github.fd4s::fs2-kafka:3.6.0
//> using dep ch.qos.logback:logback-classic:1.5.16
// ⚠️ Живая находка: JVM в базовом образе scala-cli по умолчанию использует
// НЕ UTF-8 charset для System.out (println с кириллицей превращался в "?????"
// побайтово, не просто визуальный артефакт терминала — проверено xxd).
// Фикс — явно форсировать UTF-8 для stdout/file.encoding.
//> using javaOpt -Dfile.encoding=UTF-8 -Dstdout.encoding=UTF-8 -Dstderr.encoding=UTF-8

// Стенд #8 (клиенты в разных языках), Scala: fs2-kafka (functional-streams
// обёртка над org.apache.kafka:kafka-clients, а не отдельная реализация
// протокола) — producer с ключом+идемпотентностью -> consumer в group с
// ручным коммитом.
//
// Родословная: JVM-референс "под капотом" — fs2-kafka строит java KafkaProducer/
// KafkaConsumer через Java properties и оборачивает их в fs2.Stream/cats-effect
// IO. Все фичи референсного клиента доступны (EOS, cooperative-sticky), но
// API стиль принципиально другой: не sync/callback, а декларативные потоки
// (Stream[IO, ...]) с явным control flow через cats-effect.
//
// Запуск:
//   docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/scala:/app" -w /app virtuslab/scala-cli:latest \
//     run clients.scala -- kafka1:9092,kafka2:9092,kafka3:9092

import cats.effect.{IO, IOApp, ExitCode}
import cats.syntax.all._
import fs2.kafka._
import fs2.Stream
import org.apache.kafka.clients.admin.{Admin, AdminClientConfig, NewTopic}
import java.util.Properties
import scala.jdk.CollectionConverters._
import scala.concurrent.duration._

object ClientsScala extends IOApp {

  val Topic = "demo-clients-scala"
  val Partitions = 3
  val Replication: Short = 3
  val GroupId = "demo-clients-scala-group"
  val Keys = List("order-1", "order-2", "order-3", "order-4")

  def ensureTopic(brokers: String): IO[Unit] = IO.blocking {
    val props = new Properties()
    props.put(AdminClientConfig.BOOTSTRAP_SERVERS_CONFIG, brokers)
    val admin = Admin.create(props)
    try {
      try {
        admin.deleteTopics(List(Topic).asJava).all().get(20, java.util.concurrent.TimeUnit.SECONDS)
      } catch {
        case _: Exception => () // unknown topic при первом запуске
      }
      val deadline = System.currentTimeMillis() + 10000
      while (System.currentTimeMillis() < deadline && admin.listTopics().names().get().contains(Topic)) {
        Thread.sleep(300)
      }
      admin.createTopics(List(new NewTopic(Topic, Partitions, Replication)).asJava).all().get(20, java.util.concurrent.TimeUnit.SECONDS)
      println(s"[admin] топик $Topic создан (partitions=$Partitions, rf=$Replication)")
    } finally {
      admin.close()
    }
  }

  def produce(brokers: String): IO[Int] = {
    val settings = ProducerSettings[IO, String, String]
      .withBootstrapServers(brokers)
      .withProperty("acks", "all")
      .withProperty("enable.idempotence", "true")
      .withRetries(5) // kafka-clients требует retries!=0 при enable.idempotence=true

    val records = for {
      round <- 0 until 3
      key   <- Keys
    } yield (key, s"$key-evt-$round")

    KafkaProducer.resource(settings).use { producer =>
      Stream.emits(records)
        .evalMap { case (key, value) =>
          val record = ProducerRecord(Topic, key, value)
          // ProducerResult[K,V] в fs2-kafka 3.6.0 = Chunk[(ProducerRecord[K,V],
          // RecordMetadata)] (не отдельный case class с полем .records, как в
          // более старых версиях) — живая находка при отладке этого стенда;
          // паттерн `.head.get._2` взят из исходника ProducerOps.produceOne_
          // самой библиотеки (fs2-kafka v3.6.0, KafkaProducer.scala:61).
          producer.produceOne(record).flatten.map { result =>
            val md = result.head.get._2
            println(s"  sent  key=$key partition=${md.partition()} offset=${md.offset()}")
            1
          }
        }
        .compile
        .foldMonoid
    }
  }

  case class Rec(partition: Int, offset: Long, key: String)

  def consume(brokers: String, expected: Int): IO[List[Rec]] = {
    val settings = ConsumerSettings[IO, String, String]
      .withBootstrapServers(brokers)
      .withGroupId(GroupId)
      .withAutoOffsetReset(AutoOffsetReset.Earliest)
      .withEnableAutoCommit(false)

    KafkaConsumer.stream(settings)
      .subscribeTo(Topic)
      .records
      .evalMap { committable =>
        val r = committable.record
        val rec = Rec(r.partition, r.offset, r.key)
        // ручной коммит offset ПОСЛЕ обработки каждой записи (не auto-commit)
        committable.offset.commit.as(rec)
      }
      .take(expected.toLong)
      .timeout(30.seconds)
      .compile
      .toList
  }

  def run(args: List[String]): IO[ExitCode] = {
    val brokers = args.headOption.getOrElse("kafka1:9092,kafka2:9092,kafka3:9092")
    for {
      _    <- ensureTopic(brokers)
      sent <- produce(brokers)
      _    <- IO.println(s"[producer] отправлено (acks=all, enable.idempotence=true): $sent")
      recv <- consume(brokers, sent)
      _    <- IO.println(s"[consumer] получено (group=$GroupId, manual commit): ${recv.size}")
      _    <- recv.sortBy(r => (r.partition, r.offset)).traverse_ { r =>
                 IO.println(s"  recv  (partition=${r.partition}, offset=${r.offset}, key=${r.key})")
               }
      _    <- {
                 if (sent != recv.size)
                   IO.raiseError(new IllegalStateException(s"[assert] FAIL: отправлено $sent != получено ${recv.size}"))
                 else
                   IO.println("[assert] OK: отправлено == получено")
               }
    } yield ExitCode.Success
  }
}
