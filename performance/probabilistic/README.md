# probabilistic — вероятностные структуры данных

Стенд к статье «Вероятностные структуры: Bloom-фильтры и компания».
Чистые библиотеки, без docker.

## Go
    cd go && go run . -experiment all      # bloom|hll|countmin|cuckoo|throughput
    cd go && go test ./...

## Java
    cd java && mvn -q compile exec:java -Dexec.args=all

Версии библиотек: bloom/v3 v3.7.1, axiomhq/hyperloglog v0.2.6, seiflotfy/cuckoofilter v0.0.0-20240715131351-a2f2c23f1771,
Guava 33.6.0-jre, stream-lib (com.clearspring.analytics:stream) 2.9.8.
Проверено 2026-07 на Go 1.26.3 / JDK 21.0.11 / Maven 3.9.9.
