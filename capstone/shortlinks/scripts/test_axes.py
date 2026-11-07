import json, unittest
from pathlib import Path
from contract import Contract
ROOT = Path(__file__).resolve().parents[1]
CONTRACT = Contract(outcomes=("match", "mismatch"), success="match", fields=("kind", "variant", "probe", "outcome", "detail", "cmd", "actual"))
def rows():
    return {r["variant"]: r for r in CONTRACT.read_fixture(ROOT/"fixtures/checks.jsonl")}
def actual(key):
    return rows()[key]["actual"]
class Checks(unittest.TestCase):
    def test_contract_complete(self):
        r = rows()
        hyp = json.loads((ROOT/"expected.json").read_text())
        expected = dict(hyp["checks"])  # набор ключей — исходные проверки и добавленные пересмотрами revision*
        for k in sorted(k for k in hyp if k.startswith("revision")):
            expected.update(hyp[k].get("checks", {}))
        self.assertEqual(sorted(r), sorted(expected))
        for k, v in r.items():
            self.assertEqual(v["cmd"], "python3 scripts/run-checks.py " + k)
            self.assertEqual(v["outcome"], "match")

    # статья 1
    def test_config(self):
        a = actual("config")
        self.assertEqual(a["invalid_exit_code"], 2)
        self.assertTrue(a["all_errors_reported"] and a["port_never_opened"])
        self.assertTrue(a["valid_ready"])                              # положительный контроль: верная конфигурация стартует

    def test_shutdown(self):
        a = actual("shutdown")
        self.assertEqual(a["graceful"], {"inflight_ok": 20, "inflight_failed": 0})
        self.assertEqual(a["abrupt"]["inflight_ok"], 0)                # без graceful запросы в работе обрываются
        self.assertTrue(a["readyz_503_during_delay"] and a["requests_during_delay_ok"] and a["buffered_clicks_flushed"])
        d = a["broker_down_full_buffer"]
        self.assertTrue(d["exit_within_timeout"] and d["unsent_counted"] and d["clean_exit"])   # общий срок остановки
        self.assertTrue(a["broker_up_full_buffer"]["unsent_zero"])     # контроль: брокер доступен — остатка нет
        b = a["db_blocked_request"]
        self.assertTrue(b["request_was_blocked"])                      # контроль: запрос действительно ждал базу
        self.assertTrue(b["exit_within_timeout"] and b["aborted_logged"] and b["client_got_no_success"])
        self.assertTrue(a["collector_down"]["exit_within_timeout"] and a["collector_down"]["unexported_logged"])
        self.assertTrue(a["collector_up"]["no_warning"])               # контроль: работающий Collector не даёт предупреждения

    def test_errors(self):
        a = actual("errors")
        self.assertEqual((a["invalid_url"], a["bad_json"], a["unknown_code"]), (400, 400, 404))
        self.assertEqual((a["db_down_cached_redirect"], a["db_down_uncached"]), (302, 500))
        self.assertTrue(a["internal_details_hidden"])

    def test_cache(self):
        a = actual("cache")
        self.assertEqual(a["singleflight_db_reads"], 1)
        self.assertTrue(a["no_singleflight_every_request_reads"])      # контроль: без singleflight каждый промах — в базу
        self.assertEqual(a["warm_db_reads"], 0)
        self.assertTrue(a["all_redirects_ok"] and a["waiter_cancel_and_load_deadline_tests_pass"])

    # статья 2
    def test_broker_down(self):
        a = actual("broker_down")
        self.assertTrue(a["async"]["redirects_ok_share_1"] and a["async"]["fast"])
        self.assertTrue(a["async"]["dropped_over_0"] and a["async"]["loss_visible_in_metric"])
        self.assertTrue(a["sync"]["redirects_ok_share_1"])
        self.assertFalse(a["sync"]["fast"])                            # синхронная публикация держит запрос на брокере
        self.assertTrue(a["consumer_resume"]["default_30s_over_20s"])  # молчание после перезапуска брокера
        self.assertTrue(a["consumer_resume"]["expires_5s_under_15s"])

    def test_outbox(self):
        a = actual("outbox")
        self.assertEqual(a["direct"], {"link_in_db": True, "event_seen": False})
        self.assertEqual(a["outbox"], {"link_in_db": True, "event_seen": True})

    def test_consumer(self):
        a = actual("consumer")
        self.assertTrue(a["control_exact"])
        self.assertTrue(a["crash"]["non_idempotent_overcounts"])
        self.assertTrue(a["crash"]["idempotent_exact"] and a["crash"]["idempotent_duplicates_over_0"])
        r = a["db_restart"]
        self.assertTrue(r["reconnected"])                              # контроль: соединение действительно рвалось
        self.assertTrue(r["redirects_ok_without_db"] and r["recovers_without_restart"] and r["count_exact"])

    # статья 3
    def test_image(self):
        a = actual("image")
        self.assertTrue(a["minimal_under_5pct_of_naive"] and a["minimal_has_no_shell"] and a["minimal_runs_as_nonroot"])
        self.assertTrue(a["naive_runs_as_root"])
        self.assertTrue(a["java_has_no_shell"] and a["java_runs_as_nonroot"] and a["java_over_5x_go"])

    def test_reproducible(self):
        a = actual("reproducible")
        self.assertTrue(a["binaries_identical"] and a["java_jar_identical"])
        self.assertFalse(a["image_ids_identical_plain"])               # одинаковый бинарь ещё не одинаковый образ
        self.assertTrue(a["image_ids_identical_epoch"])

    def test_sbom(self):
        a = actual("sbom")
        self.assertTrue(a["go_modules_match_buildinfo"] and a["lists_pgx_v5_11_0"])
        self.assertFalse(a["java_lists_jnats_2_26_3"])                 # fat-jar: скан образа зависимость не видит
        self.assertTrue(a["java_lists_javaagent"])
        self.assertTrue(a["cyclonedx_lists_jnats_2_26_3"] and a["cyclonedx_lists_postgresql_42_7_13"])

    def test_sign(self):
        a = actual("sign")
        self.assertTrue(a["signed_verifies"])
        self.assertTrue(a["unsigned_rejected"] and a["moved_tag_rejected"])

    def test_scan(self):
        a = actual("scan")
        self.assertTrue(a["minimal_fewer_findings_than_naive"] and a["minimal_os_findings_zero"])
        self.assertTrue(a["java_more_findings_than_go"])

    def test_chain(self):
        a = actual("chain")
        self.assertTrue(a["deploy_build_reproducible"] and a["same_digest_as_deployed_axes"] and a["sbom_of_digest_lists_pgx"])
        self.assertTrue(a["signed_digest_passes_gate"])
        self.assertTrue(a["unsigned_digest_blocked"])                  # контроль ворот
        self.assertTrue(a["running_pods_use_verified_digest"])

    def test_rollout(self):
        a = actual("rollout")
        self.assertTrue(a["control_no_errors"])                        # без выкатки поток чистый
        self.assertTrue(a["no_delay_loses_requests"])
        self.assertTrue(a["delay_no_errors"])

    def test_keda(self):
        a = actual("keda")
        self.assertTrue(a["control_stays_at_one"])
        self.assertTrue(a["same_work_within_5pct"])                    # сравнение одного и того же объёма работы
        self.assertTrue(a["scales_out_on_lag"] and a["backlog_rate_over_1_5x"] and a["returns_to_one"])

    def test_gitops(self):
        a = actual("gitops")
        self.assertTrue(a["synced_before_drift"])
        self.assertTrue(a["self_heal_reverts_drift"])
        self.assertTrue(a["control_drift_stays"] and a["control_reports_out_of_sync"])
        self.assertTrue(a["control_drift_overwritten_by_next_sync"] and a["git_change_reaches_cluster"])

    # статья 4
    def test_trace(self):
        a = actual("trace")
        p = a["propagated"]
        self.assertTrue(p["request_traces_with_consumer_share_over_0_99"] and p["consumer_spans_equal_requests"])
        self.assertTrue(p["agent_span_child_of_publish_share_over_0_99"] and p["chain_http_pg_publish_consume"] and p["jdbc_under_manual_span"])
        o = a["agent_only"]
        self.assertTrue(o["agent_span_child_of_publish_share_over_0_99"] and o["jdbc_in_request_traces_share_over_0_99"] and o["no_manual_spans"])
        for k in ("canonical_header_names", "not_propagated"):          # обрыв — ноль и у ручного спана, и у агента
            self.assertEqual(a[k], {"request_traces_with_consumer_share": 0.0, "agent_share": 0.0})
        self.assertTrue(a["foreign_parent_rejected"])                  # отрицательный контроль самой проверки связности

    def test_slo(self):
        a = actual("slo")
        self.assertTrue(a["rules_valid"])
        self.assertTrue(a["fast_burn_fires"] and a["slow_burn_fires"])
        self.assertTrue(a["normal_error_rate_silent"])                 # контроль: нормальный фон не будит
        b = a["blind_spots"]
        self.assertTrue(b["all_targets_down_fires"] and b["metrics_absent_fires"] and b["probe_failure_fires_while_ratio_silent"])

    def test_load(self):
        a = actual("load")
        self.assertTrue(a["generator_reached_rate"])                   # ступень та, что заявлена
        self.assertTrue(a["small_pool"]["errors_zero"] and a["large_pool"]["errors_zero"])
        self.assertFalse(a["small_pool_waits_for_connections"])        # на исходной частоте пул 2 ещё не узкое место
        self.assertTrue(a["large_pool_waits_less"])
        r = a["ramp_3000"]                                             # насыщение видно на повышенной ступени
        self.assertTrue(r["generator_reached_rate"] and r["small_waits_over_5pct"] and r["small_waits_over_5x_large"] and r["errors_zero"])
if __name__ == "__main__":
    unittest.main()
