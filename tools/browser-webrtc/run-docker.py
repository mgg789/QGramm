#!/usr/bin/env python3
"""Native Chromium and authenticated coturn in owned disposable containers."""
import argparse, json, os, pathlib, secrets, subprocess, tempfile, time, uuid
root = pathlib.Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser()
parser.add_argument('--output', type=pathlib.Path, help='save non-secret native browser JSON')
args = parser.parse_args()
name = 'qgramm-browser-' + uuid.uuid4().hex[:10]
image, network, turn = name + ':test', name + '-net', name + '-turn'
code = 1
try:
    subprocess.run(['docker', 'build', '-f', str(root/'tools/browser-webrtc/Dockerfile'), '-t', image, str(root)], check=True)
    subprocess.run(['docker', 'network', 'create', network], check=True, stdout=subprocess.DEVNULL)
    with tempfile.TemporaryDirectory(prefix='qgramm-browser-turn-') as directory:
        folder = pathlib.Path(directory)
        secret = secrets.token_hex(32)
        config = folder/'turn.conf'
        config.write_text('listening-port=3478\nrealm=qgramm-browser\nuse-auth-secret\nstatic-auth-secret='+secret+'\nno-cli\nno-tls\nno-dtls\nmin-port=49160\nmax-port=49200\nno-multicast-peers\nallow-loopback-peers\n')
        os.chmod(config, 0o644)
        env = folder/'env'
        env.write_text('QGRAMM_BROWSER_TURN='+secret+'\nQGRAMM_BROWSER_TURN_URL=turn:qgturn:3478?transport=udp\nQGRAMM_BROWSER_TURN_ENABLED=1\n')
        os.chmod(env, 0o600)
        subprocess.run(['docker', 'run', '-d', '--name', turn, '--network', network, '--network-alias', 'qgturn', '--mount', f'type=bind,src={config},dst=/run/turn.conf,readonly', image, 'turnserver', '-c', '/run/turn.conf'], check=True, stdout=subprocess.DEVNULL)
        time.sleep(1)
        result = subprocess.run(['docker', 'run', '--rm', '--name', name, '--network', network, '--env-file', str(env), '--shm-size=256m', image], stdout=subprocess.PIPE, text=True)
        print(result.stdout, end='')
        code = result.returncode
        if args.output:
            evidence = json.loads(result.stdout)
            evidence['command'] = 'python3 tools/browser-webrtc/run-docker.py --output docs/benchmarks/browser-webrtc-final.json'
            evidence['exitCode'] = code
            evidence['recordedAtUTC'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
            evidence['commit'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
            evidence['workingTreeDirty'] = bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=root, text=True).strip())
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(evidence, indent=2)+'\n')
finally:
    for container in [name, turn]:
        subprocess.run(['docker', 'rm', '-f', container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['docker', 'image', 'rm', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
raise SystemExit(code)
