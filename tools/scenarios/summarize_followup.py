#!/usr/bin/env python3
"""Summarize the recorded retention/fan-out follow-up without running services."""
import argparse
import hashlib
import json
import pathlib
import re
import statistics

ROOT = pathlib.Path(__file__).resolve().parents[2]


def memory_mib(raw):
    value, unit = re.fullmatch(r'([0-9.]+)([A-Za-z]+)', raw.split('/')[0].strip()).groups()
    scale = {'B': 1 / 1048576, 'KiB': 1 / 1024, 'MiB': 1, 'GiB': 1024,
             'kB': 1000 / 1048576, 'MB': 1000000 / 1048576, 'GB': 1000000000 / 1048576}
    return float(value) * scale[unit]


def summarize(path, phase):
    data = json.loads(path.read_text())
    step = next(p for p in data['phases'] if p['name'] == phase)
    samples = [s for s in data['resource_samples'] if s['phase'] == phase]
    return {
        'name': path.stem, 'source': str(path.relative_to(ROOT)),
        'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'phase': phase,
        **{k: step[k] for k in ('planned', 'accepted', 'rejected', 'skipped', 'uncertain')},
        'ack_p95_ms': step['ack']['p95_ms'], 'ack_p99_ms': step['ack']['p99_ms'],
        'delivery_p95_ms': step['delivery']['p95_ms'], 'delivery_p99_ms': step['delivery']['p99_ms'],
        'cpu_mean_percent': statistics.mean(float(s['cpu'].rstrip('%')) for s in samples),
        'ram_max_mib': max(memory_mib(s['memory']) for s in samples),
        'resource_sample_count': len(samples), 'integrity_passed': data['integrity_passed'],
        'load_pass': data['load_pass'], 'phase_slo_pass': step['slo_pass'],
        'integrity': data['integrity'], 'history': data['history'],
        'generator_sha256': data['environment']['generator_sha256'],
        'image_id': data['environment']['image_id'], 'generator_exit_code': data['generator_exit_code'],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    before = ROOT / 'docs/benchmarks/scenarios/results'
    after = ROOT / 'docs/benchmarks/retention-fanout/results'
    rows = [summarize(before / (name + '.json'), phase) for name, phase in [
        ('fanout100-qgramm-r1', 'step-2'), ('fanout100-qgramm-r2', 'step-2'), ('soak-qgramm', 'step-0')]]
    rows += [summarize(after / (name + '.json'), phase) for name, phase in [
        ('fanout-after-r1', 'step-2'), ('fanout-after-r2', 'step-2'),
        ('soak-after-r1', 'step-0'), ('soak-after-r2', 'step-0'),
        ('fanout-control-r1', 'step-2'), ('fanout-control-r2', 'step-2'),
        ('fanout-final-r1', 'step-2'), ('soak-final', 'step-0'), ('fanout-final-r2', 'step-2')]]
    result = {'aggregation': 'Phase-specific successful-request percentiles, mean Docker CPU samples, '
              'sampled RAM maximum; CPU100%=one core; historical and refreshed controls; '
              'two repeats are not confidence intervals', 'rows': rows}
    pathlib.Path(args.out).write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
