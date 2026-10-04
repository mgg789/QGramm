#!/usr/bin/env python3
"""Run the same independent MLS lifecycle with HTTPS fixture or opt-in DeepSeek.

Uses cached Docker images only. The credential is read in this launching process
and supplied over stdin, never in argv, Docker config, evidence or output.
"""
import argparse
import pathlib
import re
import subprocess
import time
import uuid


def command(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def deepseek_key(path):
    lines = pathlib.Path(path).read_text(encoding="utf-8").splitlines()
    # The operator file may contain several providers. Only consider the first
    # credential following an explicit DeepSeek heading, within three lines.
    candidates = set()
    for index, line in enumerate(lines):
        if re.search(r"\bdeepseek\b", line, re.IGNORECASE):
            for nearby in lines[index:index + 4]:
                found = re.search(r"\bsk-[A-Za-z0-9_-]{16,}\b", nearby)
                if found:
                    candidates.add(found.group())
                    break
    if len(candidates) != 1:
        raise ValueError("DeepSeek credential section unavailable or ambiguous")
    return candidates.pop()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--live", action="store_true", help="Authorize exactly two synthetic live provider conversations")
    parser.add_argument("--credential-file", help="Local operator file with a DeepSeek heading; required for --live")
    args = parser.parse_args()
    if args.live and not args.credential_file:
        parser.error("--live requires --credential-file")
    key = deepseek_key(args.credential_file) if args.live else ""
    root = pathlib.Path(__file__).resolve().parents[2]
    name = "qgramm-provider-gate-" + uuid.uuid4().hex[:12]
    network, peer = name + "-net", name + "-openmls"
    created_network = False
    started_peer = False
    try:
        command("docker", "network", "create", "--subnet", "11.98.0.0/24", network, stdout=subprocess.DEVNULL)
        created_network = True
        command("docker", "run", "--pull=never", "-d", "--name", peer, "--network", network,
                "--ip", "11.98.0.3", "qgramm-mls-interop-openmls:latest", stdout=subprocess.DEVNULL)
        started_peer = True
        for _ in range(30):
            ready = subprocess.run(["docker", "exec", peer, "nc", "-z", "localhost", "50051"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if ready.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("independent OpenMLS startup timeout")
        script = "exec go test -mod=readonly -count=1 -race -tags 'qg_e2ee qg_openai' -run '^TestProductionAIHTTPWithIndependentOpenMLSAndDBRestart$' -timeout 180s -v ./..."
        if args.live:
            script = "IFS= read -r QGRAMM_LIVE_KEY; export QGRAMM_LIVE_KEY QGRAMM_LIVE_MLS=1; " + script
        command("docker", "run", "--pull=never", "--rm", "-i", "--name", name,
                "--network", network, "--ip", "11.98.0.2",
                "-e", "QG_INTEROP_OPENMLS=" + peer + ":50051",
                "-e", "QG_INTEROP_FIXTURE_IP=11.98.0.2",
                "-v", str(root) + ":/qgramm:ro", "--entrypoint", "sh",
                "qgramm-adapter-gate-runner:latest", "-c", script,
                input=(key + "\n").encode(), timeout=240)
    finally:
        # Only resources created by this run are removed.
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if started_peer:
            subprocess.run(["docker", "rm", "-f", peer], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if created_network:
            subprocess.run(["docker", "network", "rm", network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Never print provider payloads, credentials, or subprocess input.
        raise SystemExit("Provider gate failed; sensitive details omitted")
