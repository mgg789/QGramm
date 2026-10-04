#!/usr/bin/env python3
"""Owned ephemeral Docker acceptance, no published ports or secret output."""
import os, pathlib, secrets, subprocess, tempfile, time, uuid
root = pathlib.Path(__file__).resolve().parents[2]
name = 'qgramm-webrtc-' + uuid.uuid4().hex[:10]
image, network, turn = name + ':test', name + '-net', name + '-turn'
def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)
try:
    run(['docker', 'build', '-f', str(root/'tools/webrtc/Dockerfile'), '-t', image, str(root)])
    run(['docker', 'network', 'create', network], stdout=subprocess.DEVNULL)
    with tempfile.TemporaryDirectory(prefix='qgramm-webrtc-') as directory:
        folder = pathlib.Path(directory)
        secret = secrets.token_hex(32)
        (folder/'turn.conf').write_text('listening-port=3478\nrealm=qgramm-test\nuse-auth-secret\nstatic-auth-secret='+secret+'\nno-cli\nno-tls\nno-dtls\nmin-port=49160\nmax-port=49200\nno-multicast-peers\nallow-loopback-peers\n')
        (folder/'env').write_text('QGRAMM_TEST_TURN='+secret+'\n')
        os.chmod(folder/'env', 0o600)
        os.chmod(folder/'turn.conf', 0o644)
        run(['docker', 'run', '-d', '--name', turn, '--network', network, '--network-alias', 'qgturn', '--mount', f'type=bind,src={folder}/turn.conf,dst=/run/turn.conf,readonly', image, 'turnserver', '-c', '/run/turn.conf'], stdout=subprocess.DEVNULL)
        time.sleep(1)
        run(['docker', 'run', '--rm', '--name', name+'-peers', '--network', network, '--env-file', str(folder/'env'), image])
finally:
    for container in [name+'-peers', turn]:
        subprocess.run(['docker', 'rm', '-f', container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['docker', 'image', 'rm', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
