"""Оси статьи 3: образ, воспроизводимость сборки, SBOM, подпись, скан."""

import hashlib
import json
import os
import shutil
import tarfile
import tempfile
import time
from pathlib import Path

import stack as s

ROOT = s.ROOT
IMAGES = json.loads((ROOT / "images.json").read_text())


def image(name):
    i = IMAGES[name]
    return i["ref"] + "@" + i["digest"]


def retry(argv, **kw):
    """Сборка с повторами: сбои сети при скачивании зависимостей — не результат опыта."""
    for n in range(5):
        p = s.sh(argv, check=False, **kw)
        if p.returncode == 0:
            return p
        time.sleep(10)
    raise RuntimeError("команда не прошла за 5 попыток: %r\n%s" % (argv, p.stderr[-1500:]))


def build(tag, dockerfile="Dockerfile", extra=()):
    retry(["docker", "build", "-q", "--no-cache", "-f", str(ROOT / dockerfile), "-t", tag, *extra, str(ROOT)], timeout=1800)


def binary_sha(tag, path="/app"):
    cid = s.sh(["docker", "create", tag]).stdout.strip()
    try:
        with tempfile.TemporaryDirectory() as d:
            s.sh(["docker", "cp", cid + ":" + path, d + "/f"])
            return hashlib.sha256(Path(d, "f").read_bytes()).hexdigest()
    finally:
        s.sh(["docker", "rm", cid], check=False)


def inspect(tag, fmt):
    return s.sh(["docker", "image", "inspect", "-f", fmt, tag]).stdout.strip()


def image_axis():
    build("shortlinks-service:naive", "Dockerfile.naive")
    minimal, naive = int(inspect("shortlinks-service:dev", "{{.Size}}")), int(inspect("shortlinks-service:naive", "{{.Size}}"))
    shell = s.sh(["docker", "run", "--rm", "--entrypoint", "sh", "shortlinks-service:dev", "-c", "true"], check=False).returncode
    java = int(inspect("shortlinks-consumer:dev", "{{.Size}}"))
    jshell = s.sh(["docker", "run", "--rm", "--entrypoint", "sh", "shortlinks-consumer:dev", "-c", "true"], check=False).returncode
    return {"actual": {"minimal_under_5pct_of_naive": minimal < 0.05 * naive, "minimal_has_no_shell": shell != 0,
                       "java_has_no_shell": jshell != 0,
                       "java_runs_as_nonroot": inspect("shortlinks-consumer:dev", "{{.Config.User}}").startswith("nonroot"),
                       "java_over_5x_go": java > 5 * minimal,
                       "minimal_runs_as_nonroot": inspect("shortlinks-service:dev", "{{.Config.User}}").startswith("nonroot"),
                       "naive_runs_as_root": inspect("shortlinks-service:naive", "{{.Config.User}}") in ("", "root", "0")},
            "bytes~": {"minimal": minimal, "naive": naive, "java": java}}


def reproducible():
    build("shortlinks-service:r1")
    build("shortlinks-service:r2")
    same_bin = binary_sha("shortlinks-service:r1") == binary_sha("shortlinks-service:r2")
    jars = []
    for tag in ("shortlinks-consumer:r1", "shortlinks-consumer:r2"):
        retry(["docker", "build", "-q", "--no-cache", "-t", tag, str(ROOT / "consumer-java")], timeout=1800)
        jars.append(binary_sha(tag, "/app/consumer.jar"))
    ids_plain = inspect("shortlinks-service:r1", "{{.Id}}") == inspect("shortlinks-service:r2", "{{.Id}}")
    ids = []
    for n in (1, 2):
        # OCI-архив: экспортёр docker не совмещает rewrite-timestamp с распаковкой в хранилище образов
        dest = ROOT / "out" / ("epoch%d.tar" % n)
        retry(["docker", "buildx", "build", "-q", "--no-cache", "--build-arg", "SOURCE_DATE_EPOCH=0",
              "--output", "type=oci,dest=%s,rewrite-timestamp=true" % dest, str(ROOT)],
              env=dict(os.environ, SOURCE_DATE_EPOCH="0"), timeout=1800)
        with tarfile.open(dest) as t:
            ids.append(json.load(t.extractfile("index.json"))["manifests"][0]["digest"])
        dest.unlink()
    return {"actual": {"binaries_identical": same_bin, "image_ids_identical_plain": ids_plain, "image_ids_identical_epoch": ids[0] == ids[1],
                       "java_jar_identical": jars[0] == jars[1]},
            "binary_sha256": binary_sha("shortlinks-service:r1"), "epoch_manifest_digest": ids[0]}


def sbom():
    out = s.sh(["docker", "run", "--rm", "-v", "/var/run/docker.sock:/var/run/docker.sock", image("syft"),
                "docker:shortlinks-service:dev", "-o", "json", "-q"], timeout=900).stdout
    doc = json.loads(out)
    syft = {(a["name"], a["version"]) for a in doc["artifacts"] if a["type"] == "go-module" and a["name"] != "shortlinks"
            and a["name"] != "stdlib"}
    with tempfile.TemporaryDirectory(dir=str(ROOT / "out")) as d:
        cid = s.sh(["docker", "create", "shortlinks-service:dev"]).stdout.strip()
        s.sh(["docker", "cp", cid + ":/app", d + "/app"])
        s.sh(["docker", "rm", cid], check=False)
        bi = s.sh(["docker", "run", "--rm", "-v", d + ":/b", image("golang"), "go", "version", "-m", "/b/app"]).stdout
    dep = {(l.split()[1], l.split()[2]) for l in bi.splitlines() if l.strip().startswith("dep")}
    jdoc = json.loads(s.sh(["docker", "run", "--rm", "-v", "/var/run/docker.sock:/var/run/docker.sock", image("syft"),
                            "docker:shortlinks-consumer:dev", "-o", "json", "-q"], timeout=900).stdout)
    jart = {(a["name"], a["version"]) for a in jdoc["artifacts"] if a["type"] == "java-archive"}
    # SBOM от сборки: bom.json плагина CycloneDX из этапа build Dockerfile
    retry(["docker", "build", "-q", "--target", "build", "-t", "shortlinks-consumer:build", str(ROOT / "consumer-java")], timeout=1800)
    cid = s.sh(["docker", "create", "shortlinks-consumer:build"]).stdout.strip()
    with tempfile.TemporaryDirectory(dir=str(ROOT / "out")) as d:
        s.sh(["docker", "cp", cid + ":/src/target/bom.json", d + "/bom.json"])
        s.sh(["docker", "rm", cid], check=False)
        bom = json.loads(Path(d, "bom.json").read_text())
    cdx = {(c.get("name"), c.get("version")) for c in bom.get("components", [])}
    return {"actual": {"go_modules_match_buildinfo": syft == dep, "lists_pgx_v5_11_0": ("github.com/jackc/pgx/v5", "v5.11.0") in syft,
                       "cyclonedx_lists_jnats_2_26_3": ("jnats", "2.26.3") in cdx,
                       "cyclonedx_lists_postgresql_42_7_13": ("postgresql", "42.7.13") in cdx,
                       "java_lists_jnats_2_26_3": ("jnats", "2.26.3") in jart,
                       "java_lists_javaagent": any(n.startswith("opentelemetry-javaagent") for n, _ in jart)},
            "java_archives": len(jart), "cyclonedx_components": len(cdx),
            "modules": len(syft), "syft_only": sorted("%s@%s" % x for x in syft - dep)[:10],
            "buildinfo_only": sorted("%s@%s" % x for x in dep - syft)[:10]}


NET = "capstone-reg"


def cosign(keys, *args, check=True):
    return s.sh(["docker", "run", "--rm", "--network", NET, "-v", keys + ":/k", "-e", "COSIGN_PASSWORD=", "-e", "COSIGN_YES=true",
                 image("cosign"), *args], check=check, timeout=300)


def sign():
    s.sh(["docker", "rm", "-f", "capstone-registry"], check=False)
    s.sh(["docker", "network", "rm", NET], check=False)
    s.sh(["docker", "network", "create", NET])
    s.sh(["docker", "run", "-d", "--name", "capstone-registry", "--network", NET, "--network-alias", "registry",
          "-p", "127.0.0.1:15000:5000", image("registry")])
    keys = tempfile.mkdtemp(dir=str(ROOT / "out"))
    try:
        cosign(keys, "generate-key-pair", "--output-key-prefix", "/k/cosign")

        def push(src, tag):
            s.sh(["docker", "tag", src, "127.0.0.1:15000/shortlinks:" + tag])
            out = s.sh(["docker", "push", "127.0.0.1:15000/shortlinks:" + tag]).stdout
            return out.strip().split("digest: ")[1].split()[0]
        d1 = push("shortlinks-service:dev", "v1")
        push("shortlinks-service:naive", "unsigned")
        cosign(keys, "sign", "--key", "/k/cosign.key", "--allow-http-registry", "--use-signing-config=false", "--tlog-upload=false", "registry:5000/shortlinks@" + d1)

        def verify(ref):
            return cosign(keys, "verify", "--key", "/k/cosign.pub", "--allow-http-registry", "--insecure-ignore-tlog=true",
                          "registry:5000/shortlinks" + ref, check=False).returncode == 0
        signed = verify(":v1")
        unsigned = verify(":unsigned")
        push("shortlinks-service:naive", "v1")              # тег перенесён на другой образ
        moved = verify(":v1")
        by_digest = verify("@" + d1)
    finally:
        s.sh(["docker", "rm", "-f", "capstone-registry"], check=False)
        s.sh(["docker", "network", "rm", NET], check=False)
        shutil.rmtree(keys, ignore_errors=True)
    return {"actual": {"signed_verifies": signed, "unsigned_rejected": not unsigned, "moved_tag_rejected": not moved},
            "original_digest_still_verifies": by_digest}


def trivy(tag):
    out = s.sh(["docker", "run", "--rm", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", "capstone-trivy:/root/.cache/trivy",
                image("trivy"), "image", "--scanners", "vuln", "--format", "json", "--quiet", tag], timeout=1800).stdout
    doc = json.loads(out)
    count = {"os-pkgs": 0, "lang-pkgs": 0}
    for r in doc.get("Results", []):
        count[r.get("Class", "lang-pkgs")] = count.get(r.get("Class", "lang-pkgs"), 0) + len(r.get("Vulnerabilities") or [])
    return count


def scan():
    m, n, j = trivy("shortlinks-service:dev"), trivy("shortlinks-service:naive"), trivy("shortlinks-consumer:dev")
    # находки зависят от базы уязвимостей на день прогона — её дата идёт в фикстуру рядом с числами
    db = json.loads(s.sh(["docker", "run", "--rm", "-v", "capstone-trivy:/root/.cache/trivy", image("trivy"), "version", "--format", "json"]
                         ).stdout).get("VulnerabilityDB", {})
    return {"actual": {"minimal_fewer_findings_than_naive": sum(m.values()) < sum(n.values()), "minimal_os_findings_zero": m["os-pkgs"] == 0,
                       "java_more_findings_than_go": sum(j.values()) > sum(m.values())},
            "findings~": {"minimal": m, "naive": n, "java": j}, "vuln_db_updated~": db.get("UpdatedAt", "")}


AXES = {"image": image_axis, "reproducible": reproducible, "sbom": sbom, "sign": sign, "scan": scan}
