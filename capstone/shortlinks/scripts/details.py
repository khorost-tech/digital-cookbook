"""Человекочитаемый detail строки фикстуры: из пороговых признаков actual, без шумных полей."""


def yes(b):
    return "да" if b else "нет"


def share(x):
    return ("%.2f" % x).replace(".", ",")


def describe(key, a, extra):
    if key == "config":
        return ("неверная конфигурация: код выхода %d, названы все ошибки сразу — %s, порт не открывался — %s; "
                "верная — сервис готов: %s") % (a["invalid_exit_code"], yes(a["all_errors_reported"]), yes(a["port_never_opened"]),
                                                yes(a["valid_ready"]))
    if key == "shutdown":
        g = a["graceful"]
        return ("SIGTERM при 20 запросах в работе: graceful — завершено %d, оборвано %d; выход сразу — завершено %d; "
                "во время паузы /readyz 503 — %s, запросы обслуживаются — %s; буфер кликов дописан — %s; полный буфер, брокер недоступен: "
                "уложилась в SHUTDOWN_TIMEOUT — %s, остаток посчитан — %s, выход штатный — %s; брокер доступен: остатка нет — %s") % (
            g["inflight_ok"], g["inflight_failed"], a["abrupt"]["inflight_ok"], yes(a["readyz_503_during_delay"]),
            yes(a["requests_during_delay_ok"]), yes(a["buffered_clicks_flushed"]), yes(a["broker_down_full_buffer"]["exit_within_timeout"]),
            yes(a["broker_down_full_buffer"]["unsent_counted"]), yes(a["broker_down_full_buffer"]["clean_exit"]),
            yes(a["broker_up_full_buffer"]["unsent_zero"] and a["broker_up_full_buffer"]["clean_exit"])) + (
            "; запрос ждёт базу дольше срока: ждал на момент SIGTERM — %s, выход в срок — %s, прерывание в логе — %s, клиент не получил "
            "успеха — %s") % tuple(yes(a["db_blocked_request"][k]) for k in ("request_was_blocked", "exit_within_timeout", "aborted_logged",
                                                                             "client_got_no_success")) + (
            "; Collector не отвечает: выход в срок — %s, невыгруженные трейсы в логе — %s; Collector работает: в срок — %s, без "
            "предупреждения — %s") % (yes(a["collector_down"]["exit_within_timeout"]), yes(a["collector_down"]["unexported_logged"]),
                                      yes(a["collector_up"]["exit_within_timeout"]), yes(a["collector_up"]["no_warning"]))
    if key == "errors":
        return ("неверный URL — %d, битый JSON — %d, нет кода — %d; база недоступна: ссылка из кэша — %d, вне кэша — %d; "
                "внутренние детали скрыты — %s") % (a["invalid_url"], a["bad_json"], a["unknown_code"], a["db_down_cached_redirect"],
                                                   a["db_down_uncached"], yes(a["internal_details_hidden"]))
    if key == "cache":
        return ("залп по холодной ссылке, чтение из базы 50 мс: с singleflight чтений базы %d; без него — по чтению на каждый "
                "запрос: %s; тёплый кэш — %d чтений; все редиректы 302 — %s; тесты срока загрузки и ухода ожидающего — %s") % (
            a["singleflight_db_reads"], yes(a["no_singleflight_every_request_reads"]), a["warm_db_reads"], yes(a["all_redirects_ok"]),
            yes(a["waiter_cancel_and_load_deadline_tests_pass"]))
    if key == "broker_down":
        x, y = a["async"], a["sync"]
        return ("NATS остановлен. async: все редиректы 302 — %s, быстро — %s, клики отброшены — %s, потеря видна в метрике — %s; "
                "sync: все 302 — %s, быстро — %s; consumer после перезапуска NATS молчит больше 20 с при expiresIn 30s — %s, "
                "меньше 15 с при 5s — %s") % (yes(x["redirects_ok_share_1"]), yes(x["fast"]), yes(x["dropped_over_0"]),
                                             yes(x["loss_visible_in_metric"]), yes(y["redirects_ok_share_1"]), yes(y["fast"]),
                                             yes(a["consumer_resume"]["default_30s_over_20s"]), yes(a["consumer_resume"]["expires_5s_under_15s"]))
    if key == "outbox":
        return ("падение между записью ссылки и публикацией. direct: ссылка в базе — %s, событие дошло — %s; outbox: ссылка — %s, "
                "событие — %s") % (yes(a["direct"]["link_in_db"]), yes(a["direct"]["event_seen"]), yes(a["outbox"]["link_in_db"]),
                                    yes(a["outbox"]["event_seen"]))
    if key == "consumer":
        c = a["crash"]
        return ("без падений счёт точный — %s; падение после коммита до ack: без идемпотентности пересчёт — %s, с ней счёт точный — %s, "
                "повторы распознаны — %s; PostgreSQL перезапущен: редиректы без базы — %s, consumer восстановился без перезапуска — %s, "
                "счёт точный — %s, соединение пересоздано — %s") % (yes(a["control_exact"]), yes(c["non_idempotent_overcounts"]),
                                                                    yes(c["idempotent_exact"]), yes(c["idempotent_duplicates_over_0"]),
                                                                    *(yes(a["db_restart"][k]) for k in ("redirects_ok_without_db",
                                                                      "recovers_without_restart", "count_exact", "reconnected")))
    if key == "image":
        return ("distroless меньше 5 %% наивного — %s; шелла нет: Go — %s, Java — %s; nonroot: Go — %s, Java — %s; наивный от root — %s; "
                "Java больше Go в 5 раз — %s") % (
            yes(a["minimal_under_5pct_of_naive"]), yes(a["minimal_has_no_shell"]), yes(a["java_has_no_shell"]),
            yes(a["minimal_runs_as_nonroot"]), yes(a["java_runs_as_nonroot"]), yes(a["naive_runs_as_root"]), yes(a["java_over_5x_go"]))
    if key == "reproducible":
        return ("две сборки: бинарь совпал — %s, jar совпал — %s; образ совпал без SOURCE_DATE_EPOCH — %s, с ним и "
                "rewrite-timestamp — %s") % (yes(a["binaries_identical"]), yes(a["java_jar_identical"]), yes(a["image_ids_identical_plain"]),
                                             yes(a["image_ids_identical_epoch"]))
    if key == "sbom":
        return ("Go: модули syft совпали с go version -m — %s, pgx v5.11.0 в списке — %s; Java по образу: jnats найден — %s, "
                "агент найден — %s; CycloneDX от сборки: jnats — %s, postgresql — %s") % (
            yes(a["go_modules_match_buildinfo"]), yes(a["lists_pgx_v5_11_0"]), yes(a["java_lists_jnats_2_26_3"]),
            yes(a["java_lists_javaagent"]), yes(a["cyclonedx_lists_jnats_2_26_3"]), yes(a["cyclonedx_lists_postgresql_42_7_13"]))
    if key == "sign":
        return "подписанный проходит проверку — %s; неподписанный отклонён — %s; тег, перенесённый на другой образ, отклонён — %s" % (
            yes(a["signed_verifies"]), yes(a["unsigned_rejected"]), yes(a["moved_tag_rejected"]))
    if key == "scan":
        return "у distroless находок меньше, чем у наивного, — %s; находок в пакетах ОС ноль — %s; у Java больше, чем у Go, — %s" % (
            yes(a["minimal_fewer_findings_than_naive"]), yes(a["minimal_os_findings_zero"]), yes(a["java_more_findings_than_go"]))
    if key == "rollout":
        return ("rollout restart под нагрузкой: без выкатки ошибок нет — %s; SHUTDOWN_DELAY=0 теряет запросы — %s; "
                "5s — ошибок нет: %s") % (yes(a["control_no_errors"]), yes(a["no_delay_loses_requests"]), yes(a["delay_no_errors"]))
    if key == "keda":
        return ("без ScaledObject одна реплика — %s; с ним реплик больше одной — %s; объём работы одинаков с точностью 5 %% — %s; "
                "остаток очереди разбирается быстрее в 1,5 раза и больше — %s; вернулась к одной реплике — %s") % (
            yes(a["control_stays_at_one"]), yes(a["scales_out_on_lag"]), yes(a["same_work_within_5pct"]), yes(a["backlog_rate_over_1_5x"]),
            yes(a["returns_to_one"]))
    if key == "gitops":
        return ("до правки Synced/Healthy — %s; selfHeal откатил kubectl scale — %s; без selfHeal правка осталась — %s, OutOfSync — %s, "
                "стёрта следующим коммитом — %s; коммит дошёл до кластера — %s") % (
            yes(a["synced_before_drift"]), yes(a["self_heal_reverts_drift"]), yes(a["control_drift_stays"]),
            yes(a["control_reports_out_of_sync"]), yes(a["control_drift_overwritten_by_next_sync"]), yes(a["git_change_reaches_cluster"]))
    if key == "trace":
        p, o = a["propagated"], a["agent_only"]
        return ("из 100 трейсов запросов спан consumer'а — потомок публикации больше чем в 99 %% — %s, спанов consumer'а столько же, "
                "сколько запросов, — %s, спан агента jnats — потомок публикации — %s, цепочка HTTP → PostgreSQL → публикация → consumer — %s, "
                "JDBC под ручным спаном — %s; без ручного спана: агент держит трейс — %s, JDBC в трейсах запросов — %s; имена приведены к "
                "Traceparent — доля %s, у агента %s; без передачи — %s, у агента %s") % (
            yes(p["request_traces_with_consumer_share_over_0_99"]), yes(p["consumer_spans_equal_requests"]),
            yes(p["agent_span_child_of_publish_share_over_0_99"]), yes(p["chain_http_pg_publish_consume"]), yes(p["jdbc_under_manual_span"]),
            yes(o["agent_span_child_of_publish_share_over_0_99"]), yes(o["jdbc_in_request_traces_share_over_0_99"]),
            share(a["canonical_header_names"]["request_traces_with_consumer_share"]), share(a["canonical_header_names"]["agent_share"]),
            share(a["not_propagated"]["request_traces_with_consumer_share"]), share(a["not_propagated"]["agent_share"])) + (
            "; родитель из чужого трейса отвергнут — %s" % yes(a["foreign_parent_rejected"]))
    if key == "slo":
        b = a["blind_spots"]
        return ("правила валидны — %s; всплеск будит быстрое окно — %s; утечка — медленное — %s; нормальный фон — тишина: %s; "
                "вне доли 5xx: все экземпляры недоступны — тревога %s, метрики пропали — %s, проба падает при нормальных счётчиках — %s") % (
            yes(a["rules_valid"]), yes(a["fast_burn_fires"]), yes(a["slow_burn_fires"]), yes(a["normal_error_rate_silent"]),
            yes(b["all_targets_down_fires"]), yes(b["metrics_absent_fires"]), yes(b["probe_failure_fires_while_ratio_silent"]))
    if key == "load":
        def step(r):
            share = "пул 2 ждёт больше 5 %% взятий — %s, " % yes(r["small_waits_over_5pct"]) if "small_waits_over_5pct" in r else ""
            return ("генератор держал частоту — %s, %sпул 2 ждёт больше чем в 5 раз чаще пула 20 — %s, ошибок нет — %s") % (
                yes(r["generator_reached_rate"]), share, yes(r["small_waits_over_5x_large"]), yes(r["errors_zero"]))
        return ("исходная частота: генератор держал частоту — %s, ошибок нет при малом пуле — %s и большом — %s; малый ждёт соединений "
                "больше 10 %% — %s, большой ждёт меньше — %s; вдвое выше: %s") % (
            yes(a["generator_reached_rate"]), yes(a["small_pool"]["errors_zero"]), yes(a["large_pool"]["errors_zero"]),
            yes(a["small_pool_waits_for_connections"]), yes(a["large_pool_waits_less"]), step(a["ramp_3000"]))
    if key == "chain":
        return ("две сборки для кластера дали один дайджест — %s, тот же, что выкатывают оси, — %s; SBOM этого дайджеста с pgx — %s; "
                "подписанный проходит ворота перед выкаткой — %s, неподписанный задержан — %s; живые поды — с проверенным дайджестом — %s") % (
            yes(a["deploy_build_reproducible"]), yes(a["same_digest_as_deployed_axes"]), yes(a["sbom_of_digest_lists_pgx"]),
            yes(a["signed_digest_passes_gate"]), yes(a["unsigned_digest_blocked"]), yes(a["running_pods_use_verified_digest"]))
    return ""
