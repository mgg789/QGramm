#!/usr/bin/env python3
"""Summarize private, no-forced-GC benchmark timelines without copying profiles."""
import argparse
import hashlib
import json
import pathlib
import statistics


def clock_alignment(result):
    pairs = result.get('diagnostic_clock_pairs', {})
    if not pairs.get('before') or not pairs.get('after'):
        raise ValueError('missing host/container clock calibration')
    before = min(pairs['before'], key=lambda row: row['uncertainty_ns'])
    after = min(pairs['after'], key=lambda row: row['uncertainty_ns'])
    drift = after['offset_host_minus_container_ns']-before['offset_host_minus_container_ns']
    if min(before['uncertainty_ns'], after['uncertainty_ns']) < 0:
        raise ValueError('invalid clock calibration uncertainty')
    if max(before['uncertainty_ns'], after['uncertainty_ns']) > 100_000_000 or abs(drift) > 100_000_000:
        raise ValueError('clock calibration uncertainty/drift exceeds100ms tolerance')
    return before, after, drift


def summarize(result_path, timeline_path):
    result = json.loads(result_path.read_text())
    snapshots = [json.loads(line) for line in timeline_path.read_text().splitlines() if line]
    if not snapshots or any(row.get('forced_gc') is not False for row in snapshots):
        raise ValueError('requires nonempty, no-forced-GC timeline')
    markers = [(row['at_unix_ns'], row['phase']) for row in result.get('diagnostic_phase_markers', result['resource_samples'])]
    # The host phase-file watcher polls every100ms (legacy stats every2s).
    # These boundaries are approximate, not individual request timestamps.
    before_clock, after_clock, clock_drift = clock_alignment(result)
    start = next(at for at, phase in markers if phase == 'idle')-before_clock['offset_host_minus_container_ns']
    end = next(at for at, phase in markers if phase == 'history')-after_clock['offset_host_minus_container_ns']
    if end <= start:
        raise ValueError('invalid diagnostic interval')
    rows = [row for row in snapshots if start <= row['at_unix_ns'] <= end]
    if len(rows) < 2:
        raise ValueError('timeline does not cover the idle-to-history interval')
    first, last = rows[0], rows[-1]
    records, pauses = {}, {}
    last_sequence = 0
    overwritten = 0
    for row in rows:
        diagnostic = row['diagnostics']
        entries = diagnostic['slow_records']
        if entries:
            oldest = entries[0]['sequence']
            if last_sequence and oldest > last_sequence + 1:
                overwritten += oldest - last_sequence - 1
            last_sequence = max(last_sequence, entries[-1]['sequence'])
        for entry in entries:
            if start <= entry['end_unix_nano'] <= end:
                records[entry['sequence']] = entry
        for pause in diagnostic['gc_recent_pauses']:
            if start <= pause['end_unix_nano'] <= end:
                pauses[pause['end_unix_nano']] = pause
    known = ('queue_wait', 'writer_service', 'commit', 'transaction_body',
             'query_lifetime', 'projection', 'ws_write', 'checkpoint')
    stages = {}
    for stage in known:
        entries = [entry for entry in records.values() if entry['stage'] == stage]
        durations = [entry['duration_ns'] / 1e6 for entry in entries]
        overlap = 0
        for entry in entries:
            finish = entry['end_unix_nano']
            begin = finish - entry['duration_ns']
            if any(max(begin, p['end_unix_nano']-p['duration_ns']) < min(finish, p['end_unix_nano']) for p in pauses.values()):
                overlap += 1
        stages[stage] = {'record_count': len(entries), 'max_recorded_ms': max(durations, default=0),
                         'mean_recorded_ms': statistics.mean(durations) if durations else 0,
                         'overlaps_gc_pause': overlap,
                         'failed_records': sum(bool(e.get('failed')) for e in entries)}
    def delta(name):
        return last['diagnostics'][name] - first['diagnostics'][name]
    return {
        'result_file': result_path.name, 'timeline_sha256': hashlib.sha256(timeline_path.read_bytes()).hexdigest(),
        'method': '100ms instrumented snapshots; no forced GC or sampling profilers; interval first idle-to-first history observed phase; approximate phase boundaries; no request IDs',
        'source': {key: result['environment'].get(key) for key in
                   ('server_source_commit','server_source_snapshot_sha256','server_binary_sha256','image_id','server_go',
                    'generator_binary_sha256','host_os','host_cpu','server_kernel','cpu_limit','ram_limit_gib')},
        'clock_alignment': {'before': before_clock, 'after': after_clock, 'observed_offset_drift_ns': clock_drift,
                            'uncertainty_and_drift_tolerance_ns': 100_000_000},
        'accepted': result['accepted'], 'delivered': result['delivered'],
        'history_messages': result['history_messages'], 'interval_seconds': (last['at_unix_ns']-first['at_unix_ns'])/1e9,
        'writer_interval': {key: last.get('message_writer',{}).get(key,0)-first.get('message_writer',{}).get(key,0)
                            for key in ('committed_messages','commit_count','commit_errors','queue_wait_ns','service_ns','commit_ns')},
        'sql_read_helper_calls': delta('sql_read_calls'), 'allocated_bytes': delta('total_alloc_bytes'),
        'allocations': delta('mallocs'), 'gc_cycles': delta('gc_cycles'), 'gc_pause_total_ms': delta('gc_pause_total_ns')/1e6,
        'gc_pause_max_recorded_ms': max((p['duration_ns']/1e6 for p in pauses.values()), default=0),
        'wal_sampled_max_bytes': max(r['diagnostics']['wal_bytes'] for r in rows),
        'slow_ring_overwritten_between_samples': overwritten, 'stages': stages,
        'histogram_interval': {key: [b-a for a,b in zip(first['diagnostics'][key],last['diagnostics'][key])]
            for key in ('queue_wait_jobs','writer_service_transactions','transaction_body_calls','commit_transactions',
                        'query_lifetime_calls','projection_calls','ws_write_calls','checkpoint_calls')},
        'limits': ['Stage intervals overlap and are not additive.', 'Query lifetime includes pool/preparation/execution/consumption, not isolated SQL CPU.',
                   'GC temporal overlap does not establish causation; GC assist and scheduler stalls are not directly measured.',
                   'Automatic checkpoint timing is inside commit and not separately identified.', 'Slow records >=8ms only; checkpoints record every attempt; ring capacity64.',
                   'Counter interval follows sampled boundaries; its committed-message count can differ from whole-run accepted.',
                   'Instrumented timings do not belong in primary comparisons.'],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--result', type=pathlib.Path, required=True)
    parser.add_argument('--timeline', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    args = parser.parse_args()
    args.out.write_text(json.dumps(summarize(args.result, args.timeline), indent=2)+'\n')


if __name__ == '__main__':
    main()
