"""Человекочитаемый detail строки фикстуры: из признаков actual."""

HOST = {"mac": "Mac M5", "devproxy": "devproxy Zen 4"}
LANG = {"c": "C", "go": "Go"}


def yes(b):
    return "да" if b else "нет"


def fmt(v):
    return yes(v) if isinstance(v, bool) else str(v)


def grid(a, key):
    """«Mac M5: C x, Go y; devproxy Zen 4: C x, Go y» для одного признака."""
    return "; ".join("%s: %s" % (HOST[h], ", ".join("%s %s" % (LANG[l], fmt(v[key])) for l, v in langs.items()))
                     for h, langs in a.items())


TITLES = {
    "hierarchy": [("l1_plateau_32k_vs_16k", "32K и 16K — одно плато"), ("grows_to_ram", "задержка растёт до RAM"),
                  ("ram_over_l1", "256M к 16K, раз"),
                  ("random_over_seq_per_128b_at_256m_over_10x", "случайный обход медленнее последовательного больше чем в 10 раз на 128 байт")],
    "aos_soa": [("soa_faster_large_one_field", "одно поле, 2M частиц: SoA быстрее AoS больше чем на 25%"),
                ("soa_no_gain_large_all_fields", "все поля, 2M: SoA не быстрее AoS (отношение < 1.25)"),
                ("same_small_one_field", "одно поле, 2048 частиц: вровень"),
                ("ptr_shuffled_over_3x", "срез указателей вперемешку медленнее AoS больше чем в 3 раза"),
                ("ptr_in_order_under_3x", "срез указателей по порядку — меньше чем в 3 раза"),
                ("hot_cold_split_faster_large", "горячие поля отдельно от холодных, 2M: быстрее частицы целиком больше чем в 1.5 раза")],
    "false_sharing": [("slower_than_256_at_%d" % d, "в 2+ раза медленнее, чем на 256 байтах, при %d" % d)
                      for d in (8, 16, 32, 64, 128)],
    "branch": [("branch_unsorted_over_sorted", "сумма с условием: несортированные к отсортированным"),
               ("branchless_unsorted_over_sorted", "сумма по маске: несортированные к отсортированным"),
               ("filter_unsorted_over_sorted", "отбор в массив с условием: несортированные к отсортированным"),
               ("filter_branchless_unsorted_over_sorted", "отбор без условия: несортированные к отсортированным"),
               ("filter_over_branchless_unsorted_over_2x", "несортированные: отбор с условием медленнее отбора без условия больше чем в 2 раза"),
               ("filter_over_branchless_sorted_under_1.5x", "отсортированные: отбор с условием к отбору без условия меньше 1.5")],
}

COUNTERS = [("chase_misses_per_access_16k", "cache-misses на шаг, 16K"),
            ("chase_misses_per_access_256m", "cache-misses на шаг, 256M"),
            ("aos_misses_per_elem_large", "cache-misses на частицу, AoS за кэшем"),
            ("aos_over_soa_references_large_over_4x", "cache-references AoS больше SoA в 4+ раза"),
            ("branch_misses_per_elem_unsorted", "branch-misses суммы с условием, несортированные"),
            ("branch_misses_per_elem_sorted", "то же, отсортированные"),
            ("branchless_misses_per_elem_unsorted", "branch-misses суммы по маске"),
            ("filter_misses_per_elem_unsorted", "branch-misses отбора, несортированные"),
            ("filter_misses_per_elem_sorted", "то же, отсортированные"),
            ("filter_branchless_misses_per_elem_unsorted", "branch-misses отбора без условия"),
            ("events_counted_full_time", "все события считались всё время, без мультиплексирования")]


def describe(key, a):
    if key == "control":
        hosts = lambda f: ", ".join("%s %s" % (HOST[h], fmt(v)) for h, v in a[f].items())
        return ("суммы C и Go совпадают: %s; другое зерно меняет сумму: %s; линия, байт: %s; CacheLinePad в x/sys/cpu: %s; "
                "счётчики в --privileged: %s, без него: %s") % (
            hosts("checksums_agree_c_go"), hosts("other_seed_changes_checksum"), hosts("line_bytes"),
            hosts("go_cache_line_pad"), yes(a["pmu_counts_cycles_privileged"]), yes(a["pmu_counts_cycles_unprivileged"]))
    if key == "codegen":
        names = {"branch": "сумма с условием", "filter": "отбор с условием", "filter_branchless": "отбор без условия"}
        return " | ".join("%s: условный переход на строке условия — %s; сумма в C векторизована — %s" % (
            HOST[h], "; ".join("%s: %s" % (LANG[l], ", ".join("%s %s" % (names[k], fmt(v)) for k, v in a[h]["jump_on_condition_line"][l].items()))
                              for l in ("c", "go")), fmt(a[h]["c_sum_vectorized"])) for h in a)
    if key == "counters":
        return "devproxy — " + "; ".join("%s: %s" % (t, ", ".join("%s %s" % (LANG[l], fmt(a[l][k])) for l in a)) for k, t in COUNTERS)
    return " | ".join("%s — %s" % (t, grid(a, k)) for k, t in TITLES[key])
