// mongodb/compose/init/rs-init.js — инициация replica set `rs0`.
// Запускается вручную/скриптом ПОСЛЕ `docker compose -f compose/replica-set.yml up -d`,
// напр.: docker exec -i <mongo1-контейнер> mongosh --quiet < compose/init/rs-init.js
// (Task 8: инициация кластеров — не выполняется в Task 1.)
rs.initiate({
  _id: "rs0",
  members: [
    { _id: 0, host: "mongo1:27017", priority: 2 },
    { _id: 1, host: "mongo2:27017" },
    { _id: 2, host: "mongo3:27017" },
  ],
});
