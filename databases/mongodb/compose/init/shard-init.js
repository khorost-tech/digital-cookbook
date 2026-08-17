// mongodb/compose/init/shard-init.js — регистрация шардов в кластере.
// Выполняется ПОСЛЕ `docker compose -f compose/sharded.yml up -d` и ПОСЛЕ
// rs.initiate() на csrs/shard1/shard2 каждый в своём RS (Task 8), подключение —
// к mongos: docker exec -i <mongos1-контейнер> mongosh --quiet < compose/init/shard-init.js
sh.addShard("shard1/shard1a:27017");
sh.addShard("shard2/shard2a:27017");
