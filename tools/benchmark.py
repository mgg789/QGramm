#!/usr/bin/env python3
"""Disposable Docker acceptance run; no production services or credentials."""
import argparse
import hashlib
import json
import os
import pathlib
import platform
import subprocess
import tempfile
import threading
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]


def command(*args, capture=False):
    return subprocess.run(args, cwd=ROOT, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', required=True)
    parser.add_argument('--image', help='existing matching-profile image; retained after run')
    parser.add_argument('--image-source-commit', help='recorded provenance for an existing image')
    parser.add_argument('--users', type=int, default=10000)
    parser.add_argument('--group-size', type=int, default=2)
    parser.add_argument('--duration', default='30s')
    parser.add_argument('--rate', type=int, default=100)
    parser.add_argument('--burst', default='5s')
    parser.add_argument('--burst-rate', type=int, default=1000)
    args = parser.parse_args()
    name = 'qgramm-load-' + uuid.uuid4().hex[:12]
    image = args.image or name + ':test'
    out = pathlib.Path(args.out).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    source = hashlib.sha256()
    for path in sorted(command('git', 'ls-files', capture=True).splitlines()):
        file = ROOT / path
        if file.is_file():
            source.update(path.encode() + b'\0' + file.read_bytes() + b'\0')
    samples = []
    stopped = threading.Event()
    sampler = None
    try:
        with tempfile.TemporaryDirectory(prefix=name) as directory:
            temp = pathlib.Path(directory)
            binary, env, result = temp/'bench', temp/'secrets.env', temp/'result.json'
            config = temp/'benchmark.toml'
            config.write_text('[server]\nlisten="0.0.0.0:8080"\ntrusted_proxy=true\n'
                              '[storage]\npath="/data/qgramm.db"\nfiles="/data/files"\n'
                              f'[capacity]\nexpected_concurrent_users={args.users}\n'
                              f'max_connections={max(args.users+1000,12000)}\n'
                              f'[features]\ngroups={str(args.group_size>2).lower()}\n')
            # Build context contains no benchmark secrets; they stay in temp.
            if not args.image:
                relative = pathlib.Path('configs') / (name+'.toml')
                build_config = ROOT / relative
                build_config.write_text(config.read_text())
                try:
                    command('docker','build','--build-arg','CONFIG='+str(relative),'-t',image,'.')
                finally:
                    build_config.unlink(missing_ok=True)
            command('go','build','-o',str(binary),'./cmd/qgramm-bench')
            command(str(binary),'-init','-env',str(env))
            container_env = temp/'container.env'
            container_env.write_text('\n'.join(line for line in env.read_text().splitlines()
                                                if not line.startswith('QGRAMM_BENCH_SIGNING_KEY='))+'\n')
            container_env.chmod(0o600)
            command('docker','volume','create',name)
            command('docker','run','-d','--name',name,'--cpus','4','--memory','8g',
                    '--ulimit','nofile=65536:65536','--env-file',str(container_env),
                    '-p','127.0.0.1::8080','-v',name+':/data',
                    '--mount','type=bind,src='+str(config)+',dst=/app/benchmark.toml,readonly',
                    image,'-config','/app/benchmark.toml')
            address = command('docker','port',name,'8080/tcp',capture=True).strip()
            import urllib.request
            for attempt in range(100):
                try:
                    with urllib.request.urlopen('http://'+address+'/readyz', timeout=1):
                        break
                except Exception:
                    time.sleep(.1)
            else:
                raise RuntimeError('temporary service readiness timeout')
            start = time.monotonic()

            def sample():
                while not stopped.is_set():
                    try:
                        value = command('docker','stats','--no-stream','--format','{{json .}}',name,capture=True)
                        row = json.loads(value)
                        samples.append({'elapsed_seconds': round(time.monotonic()-start,2),
                                        'cpu':row['CPUPerc'],'memory':row['MemUsage']})
                    except (subprocess.CalledProcessError, json.JSONDecodeError):
                        break
                    stopped.wait(1)

            sampler = threading.Thread(target=sample, daemon=True)
            sampler.start()
            load = subprocess.run([str(binary),'-env',str(env),'-url','http://'+address,
                    '-users',str(args.users),'-group-size',str(args.group_size),
                    '-duration',args.duration,'-rate',str(args.rate),'-burst',args.burst,
                    '-burst-rate',str(args.burst_rate),'-out',str(result)], cwd=ROOT)
            stopped.set()
            sampler.join(timeout=10)
            evidence = json.loads(result.read_text()) if result.exists() else {'note':'socket/provisioning setup failed before latency collection'}
            evidence['acceptance_passed'] = load.returncode == 0
            evidence['generator_exit_code'] = load.returncode
            inspect = json.loads(command('docker','inspect',name,capture=True))[0]
            host_cpu = (command('sysctl','-n','machdep.cpu.brand_string',capture=True).strip()
                        if platform.system()=='Darwin' else platform.processor())
            evidence['environment'] = {
                'date_utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),
                'host_os':platform.platform(),'host_cpu':host_cpu,
                'host_logical_cpus':os.cpu_count(),
                'server_kernel':command('docker','exec',name,'uname','-a',capture=True).strip(),
                'generator_go':command('go','version',capture=True).strip(),
                'image_id':inspect['Image'],'cpu_limit':4,'ram_limit_gib':8,
                'generator_source_commit':command('git','rev-parse','HEAD',capture=True).strip(),
                'server_source_commit':args.image_source_commit if args.image else command('git','rev-parse','HEAD',capture=True).strip(),
                'generator_tracked_worktree_sha256':source.hexdigest(),
                'source_dirty':bool(command('git','status','--porcelain',capture=True).strip()),
                'storage':'Docker named volume; VM virtual disk; SSD unqualified',
                'isolation':'Container quotas on shared host; generator on host; loopback port only'
            }
            evidence['resource_samples'] = samples
            evidence['resource_sampling'] = 'docker stats approximately every 2 seconds including setup; sampled maxima'
            out.write_text(json.dumps(evidence,indent=2)+'\n')
            if load.returncode:
                raise RuntimeError('load acceptance failed; diagnostic evidence saved')
    finally:
        stopped.set()
        if sampler:
            sampler.join(timeout=10)
        cleanup = [('docker','rm','-f',name),('docker','volume','rm',name)]
        if not args.image:
            cleanup.append(('docker','image','rm',image))
        for cmd in cleanup:
            subprocess.run(cmd,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
