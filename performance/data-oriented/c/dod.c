// dod.c — ядра стенда performance/data-oriented на C (контроль к Go-версии).
//
//   dod-c <ядро> [ключ=значение ...]  → одна строка JSON на stdout
//
// Ядра и параметры — те же, что в go/main.go, генератор тоже (splitmix64),
// поэтому контрольные суммы двух языков обязаны совпасть. reps=0 — только
// подготовка данных без прогона ядра: так perf stat отделяет ядро от setup.
#define _GNU_SOURCE
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static uint64_t rng_state;
static uint64_t rnd(void) {
	uint64_t z = (rng_state += 0x9E3779B97F4A7C15ULL);
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ULL;
	z = (z ^ (z >> 27)) * 0x94D049BB133111EBULL;
	return z ^ (z >> 31);
}

static int argc_g;
static char **argv_g;
static long long param(const char *key, long long def) {
	size_t n = strlen(key);
	for (int i = 2; i < argc_g; i++)
		if (!strncmp(argv_g[i], key, n) && argv_g[i][n] == '=')
			return atoll(argv_g[i] + n + 1);
	return def;
}

// CLOCK_MONOTONIC на macOS отдаёт микросекунды: ядро на 2048 элементов
// целиком помещается в один тик. Там берём счётчик в наносекундах.
static double now_ns(void) {
#ifdef __APPLE__
	return (double)clock_gettime_nsec_np(CLOCK_UPTIME_RAW);
#else
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return (double)t.tv_sec * 1e9 + (double)t.tv_nsec;
#endif
}

static int cmp_double(const void *a, const void *b) {
	double x = *(const double *)a, y = *(const double *)b;
	return (x > y) - (x < y);
}

// Прогон: прогрев (если reps > 0) и reps замеров; медиана нс на операцию.
// Один замер — inner прогонов ядра подряд, не меньше 2^24 операций: так
// короткое ядро не тонет в разрешении часов. inner считается одинаково в C,
// Go и в scripts/run-checks.py (разность счётчиков perf).
typedef uint64_t (*kernel_fn)(void);
static long long inner_runs(long long ops) { return ops >= (1 << 24) ? 1 : ((1 << 24) + ops - 1) / ops; }
static double measure(kernel_fn k, long long ops, uint64_t *check) {
	long long reps = param("reps", 7), inner = inner_runs(ops);
	if (reps == 0) return 0;
	*check = k();
	// барьер: без него clang вправе склеить повторные вызовы чистого ядра в один
	for (long long i = 1; i < inner; i++) { __asm__ volatile("" ::: "memory"); k(); }
	double *t = malloc(sizeof(double) * reps);
	for (long long r = 0; r < reps; r++) {
		double s = now_ns();
		uint64_t c = 0;
		for (long long i = 0; i < inner; i++) { __asm__ volatile("" ::: "memory"); c = k(); }
		t[r] = (now_ns() - s) / (double)(ops * inner);
		if (c != *check) { fprintf(stderr, "контрольная сумма плавает\n"); exit(2); }
	}
	qsort(t, reps, sizeof(double), cmp_double);
	double m = t[reps / 2];
	free(t);
	return m;
}

// ---- chase: случайный обход рабочего множества по одному циклу Саттоло ----
// Узлы — по одному на 128 байт (линия M5; на amd64 это две линии, из них
// трогается одна), следующий узел — индекс в слове узла.
static uint64_t *chase_buf;
static long long chase_steps;
static uint64_t k_chase(void) {
	uint64_t i = 0;
	for (long long s = 0; s < chase_steps; s++) i = chase_buf[i];
	return i;
}

// ---- seq: последовательная сумма того же объёма ----
static uint64_t *seq_buf;
static long long seq_n;
static uint64_t k_seq(void) {
	uint64_t s = 0;
	for (long long i = 0; i < seq_n; i++) s += seq_buf[i];
	return s;
}

// ---- aos / soa / ptr: сумма одного или всех восьми полей ----
typedef struct { double f[8]; } particle;
static particle *aos;
static double *soa[8];
static particle **ptrs;
static long long pn, pfields;
static uint64_t k_aos(void) {
	double s = 0;
	if (pfields == 1)
		for (long long i = 0; i < pn; i++) s += aos[i].f[0];
	else
		for (long long i = 0; i < pn; i++)
			for (int j = 0; j < 8; j++) s += aos[i].f[j];
	return (uint64_t)s;
}
static uint64_t k_soa(void) {
	double s = 0;
	if (pfields == 1)
		for (long long i = 0; i < pn; i++) s += soa[0][i];
	else
		for (long long i = 0; i < pn; i++)
			for (int j = 0; j < 8; j++) s += soa[j][i];
	return (uint64_t)s;
}
static uint64_t k_ptr(void) {
	double s = 0;
	if (pfields == 1)
		for (long long i = 0; i < pn; i++) s += ptrs[i]->f[0];
	else
		for (long long i = 0; i < pn; i++)
			for (int j = 0; j < 8; j++) s += ptrs[i]->f[j];
	return (uint64_t)s;
}

// ---- hotcold: сколько частиц пересечёт границу за шаг (x + vx >= 1000) ----
// Цикл читает два поля из восьми. aos — частица целиком, 64 байта; split —
// горячие поля отдельным массивом по 16 байт, холодные шесть — в другом.
typedef struct { double x, vx; } hot_t;
typedef struct { double f[6]; } cold_t;
static hot_t *hot;
static cold_t *cold;
static uint64_t k_cross_aos(void) {
	uint64_t n = 0;
	for (long long i = 0; i < pn; i++) n += aos[i].f[0] + aos[i].f[1] >= 1000;
	return n;
}
static uint64_t k_cross_split(void) {
	uint64_t n = 0;
	for (long long i = 0; i < pn; i++) n += hot[i].x + hot[i].vx >= 1000;
	return n;
}

// ---- fs: две нити, атомарный инкремент на расстоянии dist байт ----
static uint64_t *fs_a, *fs_b;
static long long fs_iters;
static void *fs_worker(void *p) {
	uint64_t *c = p;
	for (long long i = 0; i < fs_iters; i++) __atomic_fetch_add(c, 1, __ATOMIC_RELAXED);
	return NULL;
}
static uint64_t k_fs(void) {
	*fs_a = 0; *fs_b = 0;
	pthread_t t1, t2;
	pthread_create(&t1, NULL, fs_worker, fs_a);
	pthread_create(&t2, NULL, fs_worker, fs_b);
	pthread_join(t1, NULL);
	pthread_join(t2, NULL);
	return *fs_a + *fs_b;
}

// ---- br: сумма байтов >= 128 ----
static uint8_t *br_data;
static long long br_n;
static uint64_t k_branch(void) {
	uint64_t s = 0;
	for (long long i = 0; i < br_n; i++)
		if (br_data[i] >= 128) s += br_data[i];
	return s;
}
static uint64_t k_branchless(void) {
	uint64_t s = 0;
	for (long long i = 0; i < br_n; i++) {
		int64_t v = br_data[i];
		int64_t mask = ~((v - 128) >> 63);  // все единицы, если v >= 128
		s += (uint64_t)(v & mask);
	}
	return s;
}

// filter: отбор байтов >= 128 в выходной массив. Запись по условному индексу
// компилятор в cmov не превращает — ветка остаётся (в отличие от суммы выше).
static uint8_t *br_out;
static uint64_t k_filter(void) {
	long long k = 0;
	for (long long i = 0; i < br_n; i++)
		if (br_data[i] >= 128) br_out[k++] = br_data[i];
	return (uint64_t)k + br_out[k / 2];
}
static uint64_t k_filter_branchless(void) {
	long long k = 0;
	for (long long i = 0; i < br_n; i++) {
		br_out[k] = br_data[i];      // пишем всегда,
		k += br_data[i] >= 128;      // сдвигаемся только на подходящем
	}
	return (uint64_t)k + br_out[k / 2];
}

static void shuffle_u64(uint64_t *a, long long n) {
	for (long long i = n - 1; i > 0; i--) {
		long long j = (long long)(rnd() % (uint64_t)(i + 1));
		uint64_t t = a[i]; a[i] = a[j]; a[j] = t;
	}
}

int main(int argc, char **argv) {
	argc_g = argc; argv_g = argv;
	if (argc < 2) { fprintf(stderr, "ядро?\n"); return 1; }
	const char *k = argv[1];
	rng_state = (uint64_t)param("seed", 42);
	uint64_t check = 0;
	double ns = 0;
	long long ops = 0;

	if (!strcmp(k, "chase")) {
		long long ws = param("ws", 1 << 20), nodes = ws / 128;
		chase_buf = aligned_alloc(256, (size_t)ws);
		memset(chase_buf, 0, (size_t)ws);
		uint64_t *perm = malloc(sizeof(uint64_t) * nodes);
		for (long long i = 0; i < nodes; i++) perm[i] = (uint64_t)i;
		for (long long i = nodes - 1; i > 0; i--) {  // Саттоло: ровно один цикл
			long long j = (long long)(rnd() % (uint64_t)i);
			uint64_t t = perm[i]; perm[i] = perm[j]; perm[j] = t;
		}
		for (long long i = 0; i < nodes; i++) chase_buf[i * 16] = perm[i] * 16;
		free(perm);
		chase_steps = param("steps", 1 << 22);
		ops = chase_steps;
		ns = measure(k_chase, ops, &check);
	} else if (!strcmp(k, "seq")) {
		long long ws = param("ws", 1 << 20);
		seq_n = ws / 8;
		seq_buf = aligned_alloc(256, (size_t)ws);
		for (long long i = 0; i < seq_n; i++) seq_buf[i] = rnd() & 0xffff;
		ops = seq_n;
		ns = measure(k_seq, ops, &check);
	} else if (!strcmp(k, "aos") || !strcmp(k, "soa") || !strcmp(k, "ptr")) {
		pn = param("n", 1 << 21);
		pfields = param("fields", 1);
		ops = pn;
		// значения — целые < 1000: сумма точна в double при любом порядке
		if (!strcmp(k, "aos")) {
			aos = aligned_alloc(64, sizeof(particle) * pn);
			for (long long i = 0; i < pn; i++)
				for (int j = 0; j < 8; j++) aos[i].f[j] = (double)((i * 8 + j) % 1000);
			ns = measure(k_aos, ops, &check);
		} else if (!strcmp(k, "soa")) {
			for (int j = 0; j < 8; j++) {
				soa[j] = aligned_alloc(64, sizeof(double) * pn);
				for (long long i = 0; i < pn; i++) soa[j][i] = (double)((i * 8 + j) % 1000);
			}
			ns = measure(k_soa, ops, &check);
		} else {
			// объекты выделяются по одному и подряд; shuffled=1 перемешивает
			// порядок обхода — как срез указателей после сортировки или вставок
			ptrs = malloc(sizeof(particle *) * pn);
			for (long long i = 0; i < pn; i++) {
				ptrs[i] = malloc(sizeof(particle));
				for (int j = 0; j < 8; j++) ptrs[i]->f[j] = (double)((i * 8 + j) % 1000);
			}
			if (param("shuffled", 0)) shuffle_u64((uint64_t *)ptrs, pn);
			ns = measure(k_ptr, ops, &check);
		}
	} else if (!strcmp(k, "cross")) {
		pn = param("n", 1 << 21);
		ops = pn;
		if (param("split", 0)) {
			hot = aligned_alloc(64, sizeof(hot_t) * pn);
			cold = aligned_alloc(64, sizeof(cold_t) * pn);
			for (long long i = 0; i < pn; i++) {
				hot[i].x = (double)((i * 8) % 1000);
				hot[i].vx = (double)((i * 8 + 1) % 1000);
				for (int j = 0; j < 6; j++) cold[i].f[j] = (double)((i * 8 + j + 2) % 1000);
			}
			ns = measure(k_cross_split, ops, &check);
		} else {
			aos = aligned_alloc(64, sizeof(particle) * pn);
			for (long long i = 0; i < pn; i++)
				for (int j = 0; j < 8; j++) aos[i].f[j] = (double)((i * 8 + j) % 1000);
			ns = measure(k_cross_aos, ops, &check);
		}
	} else if (!strcmp(k, "fs")) {
		long long dist = param("dist", 8);
		uint8_t *base = aligned_alloc(256, 1024);
		memset(base, 0, 1024);
		fs_a = (uint64_t *)base;
		fs_b = (uint64_t *)(base + dist);
		fs_iters = param("iters", 20000000);
		ops = fs_iters;
		ns = measure(k_fs, ops, &check);
	} else if (!strcmp(k, "warmup")) {  // разгон частоты перед замерами
		double end = now_ns() + 2e9;
		volatile uint64_t x = 0;
		while (now_ns() < end) for (int i = 0; i < 1000000; i++) x += (uint64_t)i;
		check = x;
	} else if (!strcmp(k, "branch") || !strcmp(k, "branchless") || !strcmp(k, "filter") || !strcmp(k, "filter_branchless")) {
		br_n = param("n", 1 << 22);
		br_data = malloc((size_t)br_n);
		for (long long i = 0; i < br_n; i++) br_data[i] = (uint8_t)(rnd() & 0xff);
		if (param("sorted", 0)) {  // сортировка подсчётом
			long long cnt[256] = {0};
			for (long long i = 0; i < br_n; i++) cnt[br_data[i]]++;
			long long p = 0;
			for (int v = 0; v < 256; v++)
				for (long long c = 0; c < cnt[v]; c++) br_data[p++] = (uint8_t)v;
		}
		ops = br_n;
		br_out = malloc((size_t)br_n + 1);
		ns = measure(!strcmp(k, "branch") ? k_branch : !strcmp(k, "branchless") ? k_branchless
		             : !strcmp(k, "filter") ? k_filter : k_filter_branchless, ops, &check);
	} else {
		fprintf(stderr, "неизвестное ядро %s\n", k);
		return 1;
	}

	printf("{\"lang\":\"c\",\"kernel\":\"%s\",\"args\":\"", k);
	for (int i = 2; i < argc; i++) printf("%s%s", i > 2 ? " " : "", argv[i]);
	printf("\",\"ns_per_op\":%.4f,\"ops\":%lld,\"check\":%llu}\n", ns, ops, (unsigned long long)check);
	return 0;
}
