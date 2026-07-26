// Стенд #8 (клиенты в разных языках), C#: Confluent.Kafka (обёртка над
// librdkafka на C, через P/Invoke) -- producer с ключом+идемпотентностью ->
// consumer в group с ручным коммитом.
//
// Родословная: обёртка над librdkafka -- тот же движок, что у Python
// confluent-kafka, Rust rdkafka, Go confluent-kafka-go, C++
// modern-cpp-kafka. Нативная librdkafka тянется транзитивно через NuGet-пакет
// librdkafka.redist (платформенные .so/.dll в самом пакете) -- системный
// librdkafka-dev НЕ нужен, в отличие от Go/Rust вариантов.
//
// Запуск:
//   docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/csharp:/app" -w /app mcr.microsoft.com/dotnet/sdk:9.0 \
//     sh -c "dotnet run -c Release -- kafka1:9092,kafka2:9092,kafka3:9092"

using Confluent.Kafka;
using Confluent.Kafka.Admin;

const string Topic = "demo-clients-csharp";
const int Partitions = 3;
const short Replication = 3;
const string GroupId = "demo-clients-csharp-group";
string[] keys = { "order-1", "order-2", "order-3", "order-4" };

string brokers = args.Length > 0 ? args[0] : "kafka1:9092,kafka2:9092,kafka3:9092";

await EnsureTopicAsync(brokers);
int sent = await ProduceAsync(brokers);
Console.WriteLine($"[producer] отправлено (acks=all, enable.idempotence=true): {sent}");
var recv = Consume(brokers, sent);
Console.WriteLine($"[consumer] получено (group={GroupId}, manual commit): {recv.Count}");

if (sent != recv.Count)
{
    throw new InvalidOperationException($"[assert] FAIL: отправлено {sent} != получено {recv.Count}");
}
Console.WriteLine("[assert] OK: отправлено == получено");

async Task EnsureTopicAsync(string brokerList)
{
    using var admin = new AdminClientBuilder(new AdminClientConfig { BootstrapServers = brokerList }).Build();

    try
    {
        await admin.DeleteTopicsAsync(new[] { Topic }, new DeleteTopicsOptions { OperationTimeout = TimeSpan.FromSeconds(20) });
    }
    catch (DeleteTopicsException)
    {
        // unknown topic при первом запуске
    }

    // ⚠️ Живая находка: AdminClient.GetMetadata(topic, ...) как "проверка,
    // исчез ли топик после delete" ненадёжна -- librdkafka может неявно
    // задеть auto-create семантику при запросе метаданных конкретного
    // топика (тот же класс проблемы, что у Conn.ReadPartitions в
    // segmentio/kafka-go, см. Go-клиент этого стенда). Рабочий фикс -- НЕ
    // пробировать состояние, а просто ретраить CreateTopicsAsync с
    // фиксированным интервалом, пока предыдущий delete не пропагируется.
    await Task.Delay(1000);

    var deadline = DateTime.UtcNow.AddSeconds(15);
    Exception? lastError = null;
    while (DateTime.UtcNow < deadline)
    {
        try
        {
            await admin.CreateTopicsAsync(new[]
            {
                new TopicSpecification { Name = Topic, NumPartitions = Partitions, ReplicationFactor = Replication }
            }, new CreateTopicsOptions { OperationTimeout = TimeSpan.FromSeconds(10) });
            lastError = null;
            break;
        }
        catch (CreateTopicsException ex)
        {
            lastError = ex;
            await Task.Delay(500);
        }
    }
    if (lastError != null)
    {
        throw lastError;
    }

    Console.WriteLine($"[admin] топик {Topic} создан (partitions={Partitions}, rf={Replication})");
}

async Task<int> ProduceAsync(string brokerList)
{
    var config = new ProducerConfig
    {
        BootstrapServers = brokerList,
        Acks = Acks.All,
        EnableIdempotence = true,
    };
    using var producer = new ProducerBuilder<string, string>(config).Build();

    int count = 0;
    for (int round = 0; round < 3; round++)
    {
        foreach (var key in keys)
        {
            string value = $"{key}-evt-{round}";
            var result = await producer.ProduceAsync(Topic, new Message<string, string> { Key = key, Value = value });
            Console.WriteLine($"  sent  key={key} partition={result.Partition.Value} offset={result.Offset.Value}");
            count++;
        }
    }
    producer.Flush(TimeSpan.FromSeconds(10));
    return count;
}

List<string> Consume(string brokerList, int expected)
{
    var config = new ConsumerConfig
    {
        BootstrapServers = brokerList,
        GroupId = GroupId,
        AutoOffsetReset = AutoOffsetReset.Earliest,
        EnableAutoCommit = false,
        PartitionAssignmentStrategy = PartitionAssignmentStrategy.CooperativeSticky,
    };
    using var consumer = new ConsumerBuilder<string, string>(config).Build();
    consumer.Subscribe(Topic);

    var recs = new List<(int Partition, long Offset, string Key)>();
    var deadline = DateTime.UtcNow.AddSeconds(30);
    while (recs.Count < expected && DateTime.UtcNow < deadline)
    {
        var cr = consumer.Consume(TimeSpan.FromSeconds(1));
        if (cr == null) continue;
        recs.Add((cr.Partition.Value, cr.Offset.Value, cr.Message.Key));
        consumer.Commit(cr); // ручной синхронный коммит offset ПОСЛЕ обработки каждой записи
    }
    consumer.Close();

    if (recs.Count < expected)
    {
        throw new InvalidOperationException($"consume: таймаут, получено {recs.Count} из {expected}");
    }

    recs.Sort((a, b) => a.Partition != b.Partition ? a.Partition.CompareTo(b.Partition) : a.Offset.CompareTo(b.Offset));
    var outLines = new List<string>();
    foreach (var r in recs)
    {
        string line = $"(partition={r.Partition}, offset={r.Offset}, key={r.Key})";
        Console.WriteLine($"  recv  {line}");
        outLines.Add(line);
    }
    return outLines;
}
