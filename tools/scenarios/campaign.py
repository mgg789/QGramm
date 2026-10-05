#!/usr/bin/env python3
"""Sequential campaign; preserve failed evidence and never overwrite runs."""
import argparse
import json
import pathlib
import subprocess
import sys
import time

PROFILES = {
    'distributed': dict(users=2000, chats=1000, fanout=1, payload_bytes=256,
                        steps='500:20s,1500:20s,3000:20s,100:20s'),
    'payload4k': dict(users=2000, chats=100, fanout=1, payload_bytes=4096,
                     steps='500:20s,1500:20s,3000:20s,100:20s'),
    'fanout100': dict(users=2000, chats=10, fanout=100, payload_bytes=256,
                     steps='10:20s,50:20s,100:20s,5:20s'),
    'reconnect': dict(users=1000, chats=100, fanout=1, payload_bytes=256,
                     steps='200:10s,200:10s,200:10s', reconnect=100, offline='5s'),
}
IMAGES = {'qgramm': 'qgramm:scenario-group', 'nats': 'qgramm-scenario-nats:2.15.0',
          'centrifugo': 'qgramm-scenario-centrifugo:6.9.7'}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--generator', required=True)
    p.add_argument('--env-generator', required=True)
    p.add_argument('--out-dir', required=True)
    p.add_argument('--mode', choices=['comparison', 'idle', 'files', 'soak', 'consumerstate'], default='comparison')
    p.add_argument('--profiles', default=','.join(PROFILES), help='comma-separated comparison profiles')
    p.add_argument('--cooldown', type=float, default=15)
    a = p.parse_args()
    out = pathlib.Path(a.out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    jobs = []
    if a.mode == 'comparison':
        requested = a.profiles.split(',')
        if len(set(requested)) != len(requested) or any(profile not in PROFILES for profile in requested):
            p.error('profiles must be unique names from '+','.join(PROFILES))
        for profile in requested:
            settings = PROFILES[profile]
            for repeat, order in enumerate([list(IMAGES), list(reversed(IMAGES))], 1):
                for service in order:
                    jobs.append((f'{profile}-{service}-r{repeat}', service, settings))
    elif a.mode == 'idle':
        for service in IMAGES:
            jobs.append((f'idle10000-{service}', service,
                         dict(users=10000, chats=1, fanout=1, payload_bytes=256,
                              steps='1:1s', idle='20s')))
    elif a.mode == 'files':
        for size in [16, 64]:
            for repeat in [1, 2]:
                jobs.append((f'files{size}m-qgramm-r{repeat}', 'qgramm',
                             dict(users=2, chats=1, fanout=1, file_bytes=size*1024*1024,
                                  file_count=2, steps='1:1s')))
    elif a.mode == 'consumerstate':
        for repeat, order in enumerate([['file', 'memory'], ['memory', 'file']], 1):
            for storage in order:
                jobs.append((f'consumerstate-{storage}-nats-r{repeat}', 'nats',
                             dict(PROFILES['fanout100'], nats_consumer_memory=storage == 'memory')))
    else:
        # Derive a tested rate from both distributed repeats. If there is no
        # qualifying step, record an explicitly exploratory 100/s fallback.
        for service in IMAGES:
            qualifying = []
            for repeat in [1, 2]:
                r = json.loads((out/f'distributed-{service}-r{repeat}.json').read_text())
                qualifying.append({phase['target_messages_per_second'] for phase in r.get('phases', [])
                                   if phase['slo_pass'] and r.get('integrity_passed')})
            shared = set.intersection(*qualifying)
            rate = max(shared) if shared else 100
            jobs.append((f'soak-{service}', service,
                         dict(users=2000, chats=1000, fanout=1, payload_bytes=256,
                              steps=f'{rate}:300s', selection='both-repeat-SLO' if shared else 'exploratory-fallback')))
    for name, service, settings in jobs:
        target = out/(name+'.json')
        if target.exists():
            raise SystemExit('refusing to overwrite '+str(target))
        argv = [sys.executable, str(pathlib.Path(__file__).with_name('run.py')),
                '--service', service, '--image', 'qgramm:scenario-files' if a.mode == 'files' else IMAGES[service],
                '--generator', str(pathlib.Path(a.generator).resolve()),
                '--env-generator', str(pathlib.Path(a.env_generator).resolve()), '--out', str(target)]
        for key, value in settings.items():
            if key != 'selection':
                if isinstance(value, bool):
                    if value:
                        argv += ['--'+key.replace('_', '-')]
                else:
                    argv += ['--'+key.replace('_', '-'), str(value)]
        print(json.dumps({'start': name, 'settings': settings}), flush=True)
        completed = subprocess.run(argv)
        if target.exists():
            evidence = json.loads(target.read_text())
            evidence['campaign'] = {'name': name, 'mode': a.mode, 'settings': settings,
                                    'runner_exit_code': completed.returncode, 'cooldown_seconds': a.cooldown}
            target.write_text(json.dumps(evidence, indent=2)+'\n')
        else:
            target.write_text(json.dumps({'campaign': {'name': name, 'settings': settings},
                                         'setup_failure': 'runner produced no evidence',
                                         'comparison_excluded': True, 'integrity_passed': False}, indent=2)+'\n')
        print(json.dumps({'done': name, 'runner_exit_code': completed.returncode}), flush=True)
        time.sleep(a.cooldown)


if __name__ == '__main__':
    main()
