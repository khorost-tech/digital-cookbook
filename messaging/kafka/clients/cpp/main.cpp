// Стенд #8 (клиенты в разных языках), C++: modern-cpp-kafka (header-only
// C++ обёртка над librdkafka на C) -- producer с ключом+идемпотентностью ->
// consumer в group с ручным коммитом.
//
// Родословная: обёртка над librdkafka -- тот же движок, что у Python
// confluent-kafka, Rust rdkafka, Go confluent-kafka-go, C# Confluent.Kafka.
// modern-cpp-kafka сам по себе header-only (просто набор .h поверх C API
// librdkafka), но librdkafka-dev (нативная C-библиотека + заголовки) нужна
// в образе сборки -- та же нативная зависимость, что у остальных
// librdkafka-обёрток.
//
// Запуск:
//   docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/cpp:/app" -w /app gcc:13 sh -c "
//     apt-get update -qq && apt-get install -y -qq librdkafka-dev git >/dev/null &&
//     git clone --depth 1 https://github.com/morganstanley/modern-cpp-kafka /tmp/mck &&
//     g++ -std=c++17 -I/tmp/mck/include -O2 -o clients-cpp main.cpp -lrdkafka++ -lrdkafka -pthread &&
//     ./clients-cpp kafka1:9092,kafka2:9092,kafka3:9092"

#include <algorithm>
#include <chrono>
#include <cstdlib>
#include <iostream>
#include <string>
#include <thread>
#include <vector>

#include "kafka/AdminClient.h"
#include "kafka/KafkaConsumer.h"
#include "kafka/KafkaProducer.h"

using namespace std::chrono_literals;

namespace {

const std::string TOPIC = "demo-clients-cpp";
const int PARTITIONS = 3;
const int REPLICATION = 3;
const std::string GROUP_ID = "demo-clients-cpp-group";
const std::vector<std::string> KEYS = {"order-1", "order-2", "order-3", "order-4"};

struct Rec {
    int partition;
    long long offset;
    std::string key;
};

void ensureTopic(const std::string& brokers) {
    kafka::Properties adminProps;
    adminProps.put("bootstrap.servers", brokers);
    kafka::clients::admin::AdminClient admin(adminProps);

    auto delResult = admin.deleteTopics({TOPIC}, 20000ms);
    // игнорируем ошибку "unknown topic" при первом запуске

    std::this_thread::sleep_for(1000ms);

    kafka::Error lastError;
    auto deadline = std::chrono::steady_clock::now() + 15s;
    while (std::chrono::steady_clock::now() < deadline) {
        auto result = admin.createTopics({TOPIC}, PARTITIONS, REPLICATION, kafka::Properties(), 10000ms);
        if (!result.error) {
            lastError = kafka::Error();
            break;
        }
        lastError = result.error;
        std::this_thread::sleep_for(500ms);
    }
    if (lastError) {
        std::cerr << "CreateTopics failed: " << lastError.message() << std::endl;
        std::exit(1);
    }
    std::cout << "[admin] топик " << TOPIC << " создан (partitions=" << PARTITIONS
              << ", rf=" << REPLICATION << ")" << std::endl;
}

int produce(const std::string& brokers) {
    kafka::Properties props;
    props.put("bootstrap.servers", brokers);
    props.put("acks", "all");
    props.put("enable.idempotence", "true");
    kafka::clients::producer::KafkaProducer producer(props);

    int count = 0;
    for (int round = 0; round < 3; ++round) {
        for (const auto& key : KEYS) {
            std::string value = key + "-evt-" + std::to_string(round);
            kafka::clients::producer::ProducerRecord record(
                TOPIC, kafka::Key(key.c_str(), key.size()), kafka::Value(value.c_str(), value.size()));

            auto deliveryCb = [&](const kafka::clients::producer::RecordMetadata& metadata, const kafka::Error& error) {
                if (error) {
                    std::cerr << "produce key=" << key << ": " << error.message() << std::endl;
                    std::exit(1);
                }
                std::cout << "  sent  key=" << key << " partition=" << metadata.partition()
                          << " offset=" << metadata.offset().value() << std::endl;
            };
            // ToCopyRecordValue: value -- локальная строка, живёт только до конца итерации;
            // без копирования delivery callback мог бы увидеть уже освобождённую память.
            producer.send(record, deliveryCb, kafka::clients::producer::KafkaProducer::SendOption::ToCopyRecordValue);
            ++count;
        }
    }
    producer.close();
    return count;
}

std::vector<std::string> consume(const std::string& brokers, int expected) {
    kafka::Properties props;
    props.put("bootstrap.servers", brokers);
    props.put("group.id", GROUP_ID);
    props.put("auto.offset.reset", "earliest");
    props.put("enable.auto.commit", "false");
    props.put("partition.assignment.strategy", "cooperative-sticky");
    kafka::clients::consumer::KafkaConsumer consumer(props);
    consumer.subscribe({TOPIC});

    std::vector<Rec> recs;
    auto deadline = std::chrono::steady_clock::now() + 30s;
    while (static_cast<int>(recs.size()) < expected) {
        if (std::chrono::steady_clock::now() > deadline) {
            std::cerr << "consume: таймаут, получено " << recs.size() << " из " << expected << std::endl;
            std::exit(1);
        }
        auto records = consumer.poll(1000ms);
        for (const auto& record : records) {
            if (record.error()) continue;
            std::string key(static_cast<const char*>(record.key().data()), record.key().size());
            recs.push_back({record.partition(), record.offset(), key});
            consumer.commitSync(record); // ручной синхронный коммит offset ПОСЛЕ обработки каждой записи
        }
    }
    consumer.close();

    std::sort(recs.begin(), recs.end(), [](const Rec& a, const Rec& b) {
        return a.partition != b.partition ? a.partition < b.partition : a.offset < b.offset;
    });

    std::vector<std::string> out;
    for (const auto& r : recs) {
        std::string line = "(partition=" + std::to_string(r.partition) + ", offset=" + std::to_string(r.offset) +
                            ", key=" + r.key + ")";
        std::cout << "  recv  " << line << std::endl;
        out.push_back(line);
    }
    return out;
}

}  // namespace

int main(int argc, char** argv) {
    std::string brokers = argc > 1 ? argv[1] : "kafka1:9092,kafka2:9092,kafka3:9092";

    ensureTopic(brokers);
    int sent = produce(brokers);
    std::cout << "[producer] отправлено (acks=all, enable.idempotence=true): " << sent << std::endl;
    auto recv = consume(brokers, sent);
    std::cout << "[consumer] получено (group=" << GROUP_ID << ", manual commit): " << recv.size() << std::endl;

    if (sent != static_cast<int>(recv.size())) {
        std::cerr << "[assert] FAIL: отправлено " << sent << " != получено " << recv.size() << std::endl;
        return 1;
    }
    std::cout << "[assert] OK: отправлено == получено" << std::endl;
    return 0;
}
