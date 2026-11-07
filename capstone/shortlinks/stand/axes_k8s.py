"""Оси статьи 3 на k8s-volga: выкатка без потерь, автоскейл по лагу (KEDA), GitOps self-heal.

Кластер общий: всё живёт в namespace cookbook-capstone (его и секрет harbor-pull создаёт автор),
кластерные объекты стенд не создаёт и не меняет. Образы — linux/amd64 в Harbor, проект volga,
ссылки по дайджесту.
"""

import json
import os
import string
import time
from pathlib import Path

import stack as s

ROOT = s.ROOT
K8S = ROOT / "k8s"
NS = "cookbook-capstone"
CONTEXT = "admin@k8s-volga"
REGISTRY = "harbor.khorost.tech/volga"
KUBECONFIG = os.environ.get("STAND_KUBECONFIG", str(Path.home() / "Projects/khorost/kubeconfig-volga"))
IMAGES = {}


TRANSIENT = ("Unable to connect to the server", "i/o timeout", "TLS handshake timeout", "connection reset by peer")


def kubectl(*args, check=True, timeout=300, stdin=None):
    """kubectl в namespace стенда. Сбой связи с API-сервером — не результат опыта: до пяти повторов."""
    import subprocess
    argv = ["kubectl", "--kubeconfig", KUBECONFIG, "--context", CONTEXT, "-n", NS, *args]
    for n in range(5):
        p = subprocess.run(argv, input=stdin, capture_output=True, text=True, timeout=timeout)
        if p.returncode == 0 or not any(m in p.stderr for m in TRANSIENT):
            break
        time.sleep(10)
    if check and p.returncode:
        raise RuntimeError("kubectl failed: %r\n%s" % (args, p.stderr[-2000:]))
    return p


def render(name, **kw):
    return string.Template((K8S / name).read_text()).safe_substitute(**kw)


def apply(text):
    return kubectl("apply", "-f", "-", stdin=text)


def build_push(name, ctx, extra=(), names=(), no_cache=False):
    """Воспроизводимая сборка linux/amd64 и push: SOURCE_DATE_EPOCH и rewrite-timestamp, как в оси
    reproducible, — два прогона на одном коммите дают один дайджест. Без provenance: индексы с attestation
    в проекте volga дважды пропадали через минуты после push, одиночные манифесты — нет."""
    import os
    from axes_build import retry
    meta = ROOT / "out" / (name + ".meta")
    meta.parent.mkdir(exist_ok=True)
    refs = ",".join(["%s/%s:capstone" % (REGISTRY, name), *names])
    retry(["docker", "buildx", "build", "-q", "--platform", "linux/amd64", "--provenance=false", "--sbom=false",
           "--build-arg", "SOURCE_DATE_EPOCH=0", *extra, *(["--no-cache"] if no_cache else []), "--metadata-file", str(meta),
           "--output", 'type=image,"name=%s",push=true,unpack=false,rewrite-timestamp=true' % refs, str(ROOT / ctx)],
          env=dict(os.environ, SOURCE_DATE_EPOCH="0"), timeout=1800)
    return json.loads(meta.read_text())["containerimage.digest"]


def push_images():
    """Образы для кластера; дальше в манифестах — только ссылки по дайджесту."""
    for name, ctx, extra in (("shortlinks-service", ".", []), ("shortlinks-probe", ".", ["--build-arg", "CMD=probe"]),
                             ("shortlinks-consumer", "consumer-java", [])):
        IMAGES[name] = "%s/%s@%s" % (REGISTRY, name, build_push(name, ctx, extra))
    return IMAGES


def preflight():
    ctx = s.sh(["kubectl", "--kubeconfig", KUBECONFIG, "config", "current-context"]).stdout.strip()
    if ctx != CONTEXT:
        raise RuntimeError("контекст %s, ожидался %s" % (ctx, CONTEXT))
    if kubectl("get", "secret", "harbor-pull", check=False).returncode:
        raise RuntimeError("нет секрета harbor-pull в %s — его создаёт автор, см. README" % NS)


def rollout_wait(kind_name, timeout=300):
    kubectl("rollout", "status", kind_name, "--timeout=%ds" % timeout, timeout=timeout + 30)


def deploy(delay="5s", replicas=3, publish="async"):
    s.sh(["kubectl", "--kubeconfig", KUBECONFIG, "--context", CONTEXT, "apply", "-f", str(K8S / "00-namespace.yaml")])
    apply((K8S / "10-deps.yaml").read_text())
    for d in ("postgres", "redis", "nats"):
        rollout_wait("deploy/" + d)
    app = render("20-app.yaml", SERVICE_IMAGE=IMAGES["shortlinks-service"], CONSUMER_IMAGE=IMAGES["shortlinks-consumer"],
                 SHUTDOWN_DELAY=delay, PUBLISH_MODE=publish)
    if kubectl("get", "deploy", "service", check=False).returncode:
        # первый старт — одна реплика: миграции применяет сервис, три одновременных CREATE спорили бы
        apply(app.replace("replicas: 3", "replicas: 1", 1))
        rollout_wait("deploy/service")
    apply(app.replace("replicas: 3", "replicas: %d" % replicas, 1))
    rollout_wait("deploy/service")
    rollout_wait("deploy/consumer")


def proxy(path):
    """Путь прокси API-сервера к Service: proxy("service:8080/proxy/links")."""
    return "/api/v1/namespaces/%s/services/%s" % (NS, path)


def create_links(n):
    """Ссылки создаются через прокси API-сервера к Service — без port-forward."""
    codes = []
    for i in range(n):
        body = json.dumps({"url": "https://example.com/k8s/%d" % i})
        out = kubectl("create", "--raw", proxy("service:8080/proxy/links"), "-f", "-", stdin=body).stdout
        codes.append(json.loads(out)["code"])
    return codes


def job(name, args, timeout=600):
    kubectl("delete", "job", name, "--ignore-not-found", "--wait=true")
    apply(render("40-probe-job.yaml", NAME=name, PROBE_IMAGE=IMAGES["shortlinks-probe"], ARGS=json.dumps(args)))


def job_pod_ready(name, timeout=150):
    """Под Job появляется не сразу после apply: wait по метке без пода падает с «no matching resources»."""
    t0 = time.time()
    while not kubectl("get", "pod", "-l", "job-name=" + name, "-o", "name").stdout.strip():
        if time.time() - t0 > 60:
            raise RuntimeError("под Job %s не создан за 60 с" % name)
        time.sleep(1)
    kubectl("wait", "--for=condition=ready", "pod", "-l", "job-name=" + name, "--timeout=%ds" % timeout, timeout=timeout + 30)


def job_result(name, timeout=600):
    kubectl("wait", "--for=condition=complete", "job/" + name, "--timeout=%ds" % timeout, timeout=timeout + 30)
    out = kubectl("logs", "job/" + name).stdout.strip().splitlines()[-1]
    kubectl("delete", "job", name, "--wait=false", check=False)
    return json.loads(out)


def errors(res):
    return sum(v for k, v in res["codes"].items() if k != "302")


def rollout_run(delay, restart=True, seconds=60):
    deploy(delay=delay)
    codes = create_links(1)
    url = "http://service:8080/" + codes[0]
    job("probe-rollout", ["-url", url, "-d", "%ds" % seconds, "-c", "8", "-keepalive=false"])
    job_pod_ready("probe-rollout")
    time.sleep(10)
    t0 = time.time()
    if restart:
        kubectl("rollout", "restart", "deploy/service")
        rollout_wait("deploy/service")
    took = time.time() - t0
    res = job_result("probe-rollout")
    return {"errors~": errors(res), "requests~": sum(res["codes"].values()), "kinds~": res.get("errors", {}), "rollout_s~": round(took)}


def rollout():
    push_images() if not IMAGES else None
    preflight()
    control = rollout_run("5s", restart=False)
    no_delay = rollout_run("0s")
    delay = rollout_run("5s")
    return {"actual": {"control_no_errors": control["errors~"] == 0, "no_delay_loses_requests": no_delay["errors~"] > 0,
                       "delay_no_errors": delay["errors~"] == 0},
            "control": control, "no_delay": no_delay, "delay_5s": delay}


def pending():
    """num_pending durable analytics — из /jsz NATS через прокси API-сервера, как его видит KEDA."""
    doc = json.loads(kubectl("get", "--raw", proxy("nats:8222/proxy/jsz?consumers=true")).stdout)
    for acc in doc.get("account_details", []):
        for st in acc.get("stream_detail", []):
            for c in st.get("consumer_detail") or []:
                if c["name"] == "analytics":
                    return c["num_pending"] + c.get("num_ack_pending", 0)
    return 0


def replicas():
    return int(kubectl("get", "deploy", "consumer", "-o", "jsonpath={.status.readyReplicas}").stdout or 0)


def wait_drained(timeout=900):
    t0 = time.time()
    while pending() and time.time() - t0 < timeout:
        time.sleep(5)
    if pending():
        raise RuntimeError("очередь не опустела перед опытом")


CLICKS = 300000                                       # объём работы одинаков в обоих вариантах


def stream_seq():
    """last_seq потока SHORTLINKS: сколько событий реально опубликовано (клики могут отбрасываться буфером)."""
    doc = json.loads(kubectl("get", "--raw", proxy("nats:8222/proxy/jsz?streams=true")).stdout)
    for acc in doc.get("account_details", []):
        for st in acc.get("stream_detail", []):
            if st["name"] == "SHORTLINKS":
                return st["state"]["last_seq"]
    return 0


def job_done(name):
    return kubectl("get", "job", name, "-o", "jsonpath={.status.succeeded}", check=False).stdout.strip() == "1"


def keda_run(enabled):
    # sync: редирект ждёт подтверждения брокера, в поток попадает каждое событие — async-буфер под этой
    # нагрузкой отбрасывал десятки тысяч кликов, и варианты получали разный объём работы
    deploy(publish="sync")
    kubectl("delete", "scaledobject", "consumer", "--ignore-not-found", "--wait=true")
    kubectl("scale", "deploy/consumer", "--replicas=1")
    rollout_wait("deploy/consumer")
    wait_drained()                                    # хвост прошлых опытов исказил бы лаг
    if enabled:
        apply((K8S / "30-scaledobject.yaml").read_text())
    urls = ",".join("http://service:8080/" + c for c in create_links(20))
    seq0 = stream_seq()
    job("probe-keda", ["-url", urls, "-n", str(CLICKS), "-c", "4"])
    job_pod_ready("probe-keda")
    t0, series, load_end, backlog, drained = time.time(), [], None, None, None
    while time.time() - t0 < 1200:
        t = round(time.time() - t0)
        lag, rep = pending(), replicas()
        series.append((t, lag, rep))
        if load_end is None and job_done("probe-keda"):
            load_end, backlog = t, lag
        if load_end is not None and lag == 0 and drained is None:
            drained = t - load_end
        if drained is not None and (not enabled or rep == 1):
            break
        time.sleep(5)
    published = stream_seq() - seq0
    res = job_result("probe-keda")
    back = next((t - load_end for t, _, r in series if drained is not None and t >= load_end + drained and r == 1), None)
    if enabled:
        kubectl("delete", "scaledobject", "consumer", "--wait=true")
    return {"published~": published, "redirects~": res["codes"].get("302", 0), "load_s~": load_end,
            "backlog_at_load_end~": backlog, "drain_after_load_s~": drained,
            "backlog_rate~": None if not drained else round(backlog / drained),
            "max_replicas~": max(r for _, _, r in series), "back_to_one_after_load_s~": back, "series~": series}


def keda():
    push_images() if not IMAGES else None
    preflight()
    control, scaled = keda_run(False), keda_run(True)
    same = abs(scaled["published~"] - control["published~"]) <= 0.05 * control["published~"]
    rate = (scaled["backlog_rate~"] or 0) > 1.5 * (control["backlog_rate~"] or float("inf"))
    return {"actual": {"control_stays_at_one": control["max_replicas~"] == 1, "scales_out_on_lag": scaled["max_replicas~"] > 1,
                       "same_work_within_5pct": same, "backlog_rate_over_1_5x": rate,
                       "returns_to_one": scaled["back_to_one_after_load_s~"] is not None},
            "control": control, "keda": scaled}


def chain():
    """Один артефакт от сборки до пода: воспроизводимая сборка → SBOM → подпись → проверка → выкатка.

    Подпись и SBOM — в реестре стенда (registry:2 в сети capstone-reg) для того же дайджеста, что лежит в
    Harbor: push одним вызовом под двумя именами. В Harbor стенд подписи не пишет, admission-проверки в
    кластере нет — ворота стоят перед kubectl apply.
    """
    import shutil
    import tempfile
    import axes_build as b
    push_images() if not IMAGES else None
    preflight()
    local = "127.0.0.1:15000/shortlinks:chain"
    s.sh(["docker", "rm", "-f", "capstone-registry"], check=False)
    s.sh(["docker", "network", "rm", b.NET], check=False)
    s.sh(["docker", "network", "create", b.NET])
    s.sh(["docker", "run", "-d", "--name", "capstone-registry", "--network", b.NET, "--network-alias", "registry",
          "-p", "127.0.0.1:15000:5000", b.image("registry")])
    keys = tempfile.mkdtemp(dir=str(ROOT / "out"))
    try:
        d1 = build_push("shortlinks-service", ".", names=[local], no_cache=True)
        d2 = build_push("shortlinks-service", ".", names=[local], no_cache=True)
        deployed_before = IMAGES["shortlinks-service"].split("@")[1]
        sbom = json.loads(s.sh(["docker", "run", "--rm", "--network", b.NET, "-e", "SYFT_REGISTRY_INSECURE_USE_HTTP=true", b.image("syft"),
                                "registry:registry:5000/shortlinks@" + d1, "-o", "json", "-q"], timeout=900).stdout)
        pgx = any(a["name"] == "github.com/jackc/pgx/v5" for a in sbom["artifacts"])
        b.cosign(keys, "generate-key-pair", "--output-key-prefix", "/k/cosign")
        b.cosign(keys, "sign", "--key", "/k/cosign.key", "--allow-http-registry", "--use-signing-config=false", "--tlog-upload=false",
                 "registry:5000/shortlinks@" + d1)
        # контроль ворот: неподписанный дайджест в том же реестре
        unsigned = build_push("shortlinks-probe", ".", ["--build-arg", "CMD=probe"], names=["127.0.0.1:15000/shortlinks:unsigned"])

        def gate(digest):
            return b.cosign(keys, "verify", "--key", "/k/cosign.pub", "--allow-http-registry", "--insecure-ignore-tlog=true",
                            "registry:5000/shortlinks@" + digest, check=False).returncode == 0
        signed_ok, unsigned_ok = gate(d1), gate(unsigned)
        running = []
        if signed_ok:
            IMAGES["shortlinks-service"] = "%s/shortlinks-service@%s" % (REGISTRY, d1)
            deploy()
            kubectl("rollout", "restart", "deploy/service")
            rollout_wait("deploy/service")
            # поды старой версии ещё дренируются (Running с deletionTimestamp) — считаем только живые
            for _ in range(60):
                pods = json.loads(kubectl("get", "pods", "-l", "app=service", "-o", "json").stdout)["items"]
                live = [x for x in pods if not x["metadata"].get("deletionTimestamp") and x["status"].get("phase") == "Running"]
                if len(live) == 3 and len(pods) == 3:
                    break
                time.sleep(2)
            running = [x["status"]["containerStatuses"][0]["imageID"].split("@")[-1] for x in live]
    finally:
        s.sh(["docker", "rm", "-f", "capstone-registry"], check=False)
        s.sh(["docker", "network", "rm", b.NET], check=False)
        shutil.rmtree(keys, ignore_errors=True)
    return {"actual": {"deploy_build_reproducible": d1 == d2, "same_digest_as_deployed_axes": d1 == deployed_before,
                       "sbom_of_digest_lists_pgx": pgx, "signed_digest_passes_gate": signed_ok, "unsigned_digest_blocked": not unsigned_ok,
                       "running_pods_use_verified_digest": bool(running) and all(r == d1 for r in running)},
            "digest": d1, "running_pods": len(running)}


ARGOCD_URL = "https://raw.githubusercontent.com/argoproj/argo-cd/v3.4.1/manifests/namespace-install.yaml"
ARGOCD_SHA256 = "baf0520c3dfd1a43974ff600099377237cf38155b63c767b7339edb9af53380d"   # тот же файл, что у kubernetes/gitops
GIT_IMAGE = "buildpack-deps:bookworm-scm@sha256:877e9e4d949edfbcbedabc3a2d7ab593955fee5d6d0777adf3a991eb30c750d8"


def argocd_install():
    """ArgoCD v3.4.1 (как боевой в ns argocd) без CRD и без кластерных ролей — в namespace стенда.

    CRD argoproj.io в кластере общие и принадлежат боевому ArgoCD: стенд их только использует.
    """
    import hashlib
    import urllib.request
    raw = ROOT / "out" / "argocd-namespace-install.yaml"
    if not raw.exists():
        for opener in (urllib.request.build_opener(),
                       urllib.request.build_opener(urllib.request.ProxyHandler({"https": "http://s9.khorost.tech:3128"}))):
            try:
                raw.write_bytes(opener.open(ARGOCD_URL, timeout=60).read())
                break
            except OSError:
                continue
    body = raw.read_bytes()
    if hashlib.sha256(body).hexdigest() != ARGOCD_SHA256:
        raise RuntimeError("манифест ArgoCD не тот: sha256 не совпал")
    text = body.decode()
    kinds = {l.split()[1] for l in text.splitlines() if l.startswith("kind: ")}
    if {"CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding"} & kinds:
        raise RuntimeError("в namespace-install есть кластерные объекты — стенд их не ставит")
    # public.ecr.aws с узлов k8s-volga недоступен; меняется только реестр, версия redis та же
    text = text.replace("public.ecr.aws/docker/library/redis:", "docker.io/library/redis:")
    apply(text)
    for f in ("11-rbac.yaml", "15-cluster-scope.yaml"):
        apply((K8S / "argocd" / f).read_text())
    for d in ("argocd-repo-server", "argocd-redis", "argocd-server"):
        rollout_wait("deploy/" + d)
    kubectl("rollout", "status", "statefulset/argocd-application-controller", "--timeout=300s", timeout=330)


def git_server(app_text):
    kubectl("delete", "configmap", "git-seed", "--ignore-not-found")
    kubectl("create", "configmap", "git-seed", "--from-file=app.yaml=/dev/stdin", stdin=app_text)
    kubectl("delete", "deploy", "git-server", "--ignore-not-found", "--wait=true")
    apply((K8S / "argocd" / "50-git-server.yaml").read_text())
    rollout_wait("deploy/git-server")


def git_commit(script, message):
    """Коммит в репозиторий стенда из пода в namespace: к git-серверу пускают только оттуда."""
    cmd = ("git config --global user.email stand@khorost.tech; git config --global user.name 'digital-cookbook stand'; "
           "git clone -q git://git-server/shortlinks.git /tmp/r && cd /tmp/r && %s && git commit -qam %s && "
           "git push -q origin main && git log --oneline -1" % (script, json.dumps(message)))
    pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "git-client"},
           "spec": {"restartPolicy": "Never",
                    "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000,
                                        "seccompProfile": {"type": "RuntimeDefault"}},
                    "containers": [{"name": "git", "image": GIT_IMAGE, "env": [{"name": "HOME", "value": "/tmp"}],
                                    "command": ["bash", "-euo", "pipefail", "-c", cmd],
                                    "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
                                    "resources": {"requests": {"cpu": "20m", "memory": "32Mi"}, "limits": {"memory": "256Mi"}}}]}}
    kubectl("delete", "pod", "git-client", "--ignore-not-found", "--wait=true")
    apply(json.dumps(pod))
    kubectl("wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/git-client", "--timeout=120s", timeout=150)
    out = kubectl("logs", "git-client").stdout.strip()
    kubectl("delete", "pod", "git-client", "--wait=false")
    return out


def app_status():
    st = json.loads(kubectl("get", "application", "shortlinks", "-o", "json").stdout).get("status", {})
    return st.get("sync", {}).get("status"), st.get("health", {}).get("status")


def app_synced_to(sha):
    """Application синхронизирован на ревизию коммита и операция синхронизации завершилась успешно."""
    st = json.loads(kubectl("get", "application", "shortlinks", "-o", "json").stdout).get("status", {})
    op = st.get("operationState", {})
    return (op.get("syncResult", {}).get("revision", "").startswith(sha) and op.get("phase") == "Succeeded")


def service_replicas():
    return int(kubectl("get", "deploy", "service", "-o", "jsonpath={.spec.replicas}").stdout)


def service_env(name):
    env = json.loads(kubectl("get", "deploy", "service", "-o", "jsonpath={.spec.template.spec.containers[0].env}").stdout)
    return next((e["value"] for e in env if e["name"] == name), None)


def wait_for(cond, timeout):
    t0 = time.time()
    while time.time() - t0 < timeout:
        if cond():
            return round(time.time() - t0)
        time.sleep(2)
    return None


def gitops_run(self_heal, watch=90):
    kubectl("delete", "application", "shortlinks", "--ignore-not-found", "--wait=true")
    deploy()
    app_text = render("20-app.yaml", SERVICE_IMAGE=IMAGES["shortlinks-service"], CONSUMER_IMAGE=IMAGES["shortlinks-consumer"],
                      SHUTDOWN_DELAY="5s", PUBLISH_MODE="async")
    git_server(app_text)
    # у Application нет финализатора resources-finalizer: его удаление не сносит сервис
    apply(render("argocd/60-application.yaml", SELF_HEAL="true" if self_heal else "false"))
    synced = wait_for(lambda: app_status() == ("Synced", "Healthy"), 300)
    before = app_status()
    kubectl("scale", "deploy/service", "--replicas=1")             # ручная правка мимо git
    healed = wait_for(lambda: service_replicas() == 3, watch)
    after = (service_replicas(), app_status()[0])
    commit = git_commit("sed -i 's/value: \"5s\"}/value: \"6s\"}/' apps/shortlinks/app.yaml", "SHUTDOWN_DELAY 6s")
    kubectl("annotate", "application", "shortlinks", "argocd.argoproj.io/refresh=normal", "--overwrite")
    sha = commit.split()[0] if commit else "-"
    # ждать завершения синхронизации именно этого коммита: признак «env уже 6s» мог сработать на старом состоянии
    commit_synced = wait_for(lambda: app_synced_to(sha), 180)
    applied = wait_for(lambda: service_env("SHUTDOWN_DELAY") == "6s", 60) if commit_synced is not None else None
    # синхронизация применяет манифест целиком: ручная правка без self-heal живёт до коммита
    replicas_after_commit = service_replicas()
    kubectl("delete", "application", "shortlinks", "--wait=true")
    return {"synced_before_drift": before == ("Synced", "Healthy"), "first_sync_s~": synced, "healed_after_s~": healed,
            "after_watch": {"replicas": after[0], "sync": after[1]}, "git_change_applied_s~": commit_synced if applied is not None else None,
            "replicas_after_commit": replicas_after_commit, "commit~": commit}


def gitops():
    push_images() if not IMAGES else None
    preflight()
    # ArgoCD ставится заранее отдельным шагом (README, «Подготовка»): установка — не измерение
    if kubectl("get", "statefulset", "argocd-application-controller", check=False).returncode:
        raise RuntimeError("ArgoCD стенда не установлен: python3 stand/axes_k8s.py install")
    heal, control = gitops_run(True), gitops_run(False)
    return {"actual": {"synced_before_drift": heal["synced_before_drift"] and control["synced_before_drift"],
                       "self_heal_reverts_drift": heal["healed_after_s~"] is not None,
                       "control_drift_stays": control["healed_after_s~"] is None and control["after_watch"]["replicas"] == 1,
                       "control_reports_out_of_sync": control["after_watch"]["sync"] == "OutOfSync",
                       "git_change_reaches_cluster": heal["git_change_applied_s~"] is not None
                       and control["git_change_applied_s~"] is not None,
                       "control_drift_overwritten_by_next_sync": control["replicas_after_commit"] == 3},
            "self_heal": heal, "control": control}


AXES = {"chain": chain, "rollout": rollout, "keda": keda, "gitops": gitops}


if __name__ == "__main__":
    import sys
    if sys.argv[1:] == ["install"]:
        preflight()
        argocd_install()
