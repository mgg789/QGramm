#!/usr/bin/env python3
"""Prepare verified official Linux/arm64 benchmark server binaries and images."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tarfile
import urllib.request

RELEASES = {
    "nats": {
        "repository": "nats-io/nats-server", "tag": "v2.15.0",
        "asset": "nats-server-v2.15.0-linux-arm64.tar.gz", "binary": "nats-server",
        "asset_sha256": "cdc208f5a3f42963a52b6ab06ef65626bb870315dc936e26ba571780c6351112",
        "binary_sha256": "9f9ac35b87e0e415802adfc177e073eb4c78050d5c368b6d18f8db571dc93496",
        "image": "qgramm-scenario-nats:2.15.0",
    },
    "centrifugo": {
        "repository": "centrifugal/centrifugo", "tag": "v6.9.7",
        "asset": "centrifugo_6.9.7_linux_arm64.tar.gz", "binary": "centrifugo",
        "asset_sha256": "3f8fcb0022b5a793be5569f681df7a9936bfa65bfdfe86445fdc217e32fe06a4",
        "binary_sha256": "af6800f2ce52846a2240b01131411a8d3fc6ecb9780ce8a37ca66e66b145f0a9",
        "image": "qgramm-scenario-centrifugo:6.9.7",
    },
}

def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()

def verify(path, expected):
    actual = digest(path)
    if actual != expected:
        raise ValueError(f"SHA-256 mismatch: {path.name}")

def select_binary(archive, name):
    """Read one regular member; never extract archive paths or links."""
    with tarfile.open(archive, "r:gz") as tar:
        matches = [m for m in tar.getmembers() if pathlib.PurePosixPath(m.name).name == name]
        if len(matches) != 1:
            raise ValueError("archive must contain exactly one named binary")
        member = matches[0]
        path = pathlib.PurePosixPath(member.name)
        if path.is_absolute() or ".." in path.parts or not member.isfile():
            raise ValueError("unsafe binary archive member")
        if not 0 < member.size <= 64 * 1024 * 1024:
            raise ValueError("unexpected binary size")
        source = tar.extractfile(member)
        if source is None:
            raise ValueError("binary member unavailable")
        binary = source.read(member.size + 1)
        if len(binary) != member.size or binary[:4] != b"\x7fELF":
            raise ValueError("expected complete Linux ELF binary")
        return binary

def download(url, target):
    temporary = target.with_suffix(target.suffix + ".partial")
    request = urllib.request.Request(url, headers={"User-Agent": "QGramm-scenario-build"})
    try:
        with urllib.request.urlopen(request, timeout=60) as source, temporary.open("wb") as output:
            total = 0
            while block := source.read(1024 * 1024):
                total += len(block)
                if total > 128 * 1024 * 1024:
                    raise ValueError("release asset exceeds download bound")
                output.write(block)
        temporary.replace(target)
    finally:
        temporary.unlink(missing_ok=True)

def prepare(service, directory, build=False, offline=False):
    release = RELEASES[service]
    directory.mkdir(parents=True, exist_ok=True)
    asset = directory / release["asset"]
    release_url = f'https://github.com/{release["repository"]}/releases/tag/{release["tag"]}'
    asset_url = f'https://github.com/{release["repository"]}/releases/download/{release["tag"]}/{release["asset"]}'
    if not asset.exists():
        if offline:
            raise ValueError(f"offline asset missing: {asset}")
        download(asset_url, asset)
    verify(asset, release["asset_sha256"])
    data = select_binary(asset, release["binary"])
    if hashlib.sha256(data).hexdigest() != release["binary_sha256"]:
        raise ValueError("binary SHA-256 mismatch")
    binary = directory / release["binary"]
    binary.write_bytes(data)
    binary.chmod(0o755)
    dockerfile = directory / "Dockerfile"
    dockerfile.write_text(
        'FROM alpine:3.22\n'
        f'COPY {release["binary"]} /usr/local/bin/{release["binary"]}\n'
        f'LABEL scenario.release="{release["tag"]}" scenario.source="{release_url}"\n'
        f'ENTRYPOINT ["{release["binary"]}"]\n', encoding="utf-8")
    provenance = dict(release, asset_url=asset_url, url=release_url, platform="linux/arm64",
                      dockerfile_sha256=digest(dockerfile), image_built=False)
    if build:
        subprocess.run(["docker", "build", "--platform", "linux/arm64", "-t", release["image"], str(directory)], check=True)
        provenance["image_id"] = subprocess.check_output(
            ["docker", "image", "inspect", release["image"], "--format", "{{.Id}}"], text=True).strip()
        provenance["base_image_id"] = subprocess.check_output(
            ["docker", "image", "inspect", "alpine:3.22", "--format", "{{.Id}}"], text=True).strip()
        provenance["image_built"] = True
    (directory / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")
    return provenance

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service", choices=["nats", "centrifugo", "all"], default="all")
    parser.add_argument("--work-dir", type=pathlib.Path, required=True, help="private build/download directory outside the source tree")
    parser.add_argument("--build", action="store_true", help="also invoke Docker; default only prepares verified build inputs")
    parser.add_argument("--offline", action="store_true", help="require already downloaded pinned archives; never use network")
    args = parser.parse_args()
    services = RELEASES if args.service == "all" else [args.service]
    for service in services:
        result = prepare(service, args.work_dir / service, args.build, args.offline)
        print(json.dumps({"service": service, "binary_sha256": result["binary_sha256"], "image_built": result["image_built"]}))

if __name__ == "__main__":
    main()
