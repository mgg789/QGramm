#!/usr/bin/env python3
"""Summarize recorded successful-request latency and phase-labelled samples."""
import argparse, json, pathlib, re, statistics

def memory_mib(value):
    match = re.match(r'([\d.]+)([A-Za-z]+)', value.split('/')[0].strip())
    if not match: raise ValueError(value)
    return float(match[1]) * {'B':1/(1024**2),'KiB':1/1024,'MiB':1,'GiB':1024}[match[2]]

def summarize(path):
    d=json.loads(path.read_text()); groups={}
    for sample in d['resource_samples']:
        groups.setdefault(sample.get('phase','all'),[]).append(sample)
    phases={}
    for phase,samples in groups.items():
        cpu=[float(s['cpu'].rstrip('%')) for s in samples]
        ram=[memory_mib(s['memory']) for s in samples]
        phases[phase]={'samples':len(samples),'cpu_mean_percent':round(statistics.mean(cpu),3),'cpu_median_percent':round(statistics.median(cpu),3),'cpu_mean_excluding_first_sample_percent':round(statistics.mean(cpu[1:]),3) if len(cpu)>1 else None,'cpu_max_percent':max(cpu),'memory_mean_mib':round(statistics.mean(ram),3),'memory_max_mib':max(ram)}
    latency={k:v for k,v in d.items() if k.endswith('_ms')}
    integrity={k:d.get(k) for k in ['offered','accepted','delivered','history_messages','failed','backpressure','generator_skipped','unexpected_disconnects','websocket_errors']}
    if 'phases' in d:
        latency={p['name']:{'ack_ms':p['ack_ms'],'delivery_ms':p['delivery_ms']} for p in d['phases']}
        integrity={k:sum(p[k] for p in d['phases']) for k in ['offered','accepted','delivered','duplicate','publish_errors']}
        integrity['connection_or_receive_errors']=d['connection_or_receive_errors']
    return {'file':str(path),'acceptance_passed':d.get('acceptance_passed'),'comparison_excluded':d.get('comparison_excluded',False),'comparison_exclusion_reason':d.get('comparison_exclusion_reason'),'instrumented':d.get('instrumented',False),'embedded_redis':d.get('embedded_redis',False),'idle_subscriptions':d.get('idle_subscriptions',False),'response_mode':d.get('response_mode'), 'latency':latency,'integrity':integrity,'resources':phases}

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('files',nargs='+',type=pathlib.Path);a=p.parse_args()
    print(json.dumps([summarize(path) for path in a.files],indent=2))
