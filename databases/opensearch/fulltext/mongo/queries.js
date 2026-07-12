// Эквивалент OpenSearch match(body:"поиск") / PG plainto_tsquery('поиск'):
// $text-поиск + сортировка по встроенной релевантности textScore.
db.articles.find(
  { $text: { $search: "поиск" } },
  { title: 1, score: { $meta: "textScore" } }
).sort({ score: { $meta: "textScore" } }).forEach(d => {
  print(d._id + "  score=" + d.score.toFixed(4) + "  " + d.title);
});
