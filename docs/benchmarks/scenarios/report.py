import json
import pathlib
import statistics
import subprocess

root = pathlib.Path(__file__).parent
subprocess.run(['python3', str(root/'make-summary.py')], check=True, stdout=subprocess.DEVNULL)
s = json.loads((root/'summary.json').read_text())
phases = [r for r in s['phase_rows'] if not r['comparison_excluded']]
runs = [r for r in s['runs'] if not r.get('comparison_excluded')]

def span(values, digits=2):
    values = [v for v in values if v is not None]
    if not values:
        return '—'
    lo, hi = min(values), max(values)
    return f'{lo:.{digits}f}' if lo == hi else f'{lo:.{digits}f}–{hi:.{digits}f}'

def table(headers, rows):
    assert all(len(row)==len(headers) for row in rows), headers
    return '\n| '+' | '.join(headers)+' |\n| '+' | '.join(['---']*len(headers))+' |\n'+''.join('| '+' | '.join(map(str,row))+' |\n' for row in rows)

parts = []
for profile, rate in [('distributed',500),('distributed',1500),('distributed',3000),('payload4k',500),('payload4k',3000),('fanout100',100)]:
    rows = []
    for service in ['qgramm','nats','centrifugo']:
        matches = [r for r in phases if r['run'].startswith(profile+'-'+service+'-') and r['target_messages_per_second']==rate]
        if not matches: continue
        rows.append([service, span([r['accepted']/r['planned']*100 for r in matches]),span([r['ack']['p95_ms'] for r in matches]),span([r['ack']['p99_ms'] for r in matches]),span([r['delivery']['p95_ms'] for r in matches]),span([r['delivery']['p99_ms'] for r in matches]),span([r['resources']['cpu_mean_percent'] for r in matches],1),span([r['resources']['memory_max_mib'] for r in matches],1),span([r['skipped'] for r in matches],0),span([r['rejected'] for r in matches],0),all(r['integrity_passed'] for r in matches)])
    parts.append(f'\n## {profile}: {rate} original messages/s\n'+table(['Service','Accepted/planned %','ACK p95 ms','ACK p99 ms','Delivery p95 ms','Delivery p99 ms','CPU %','RAM MiB','Skipped','Rejected','Run integrity'], rows))

rows=[]
for service in ['qgramm','nats','centrifugo']:
    matches=[r for r in runs if r['name'].startswith('reconnect-'+service)]
    rows.append([service,span([r['integrity']['accepted'] for r in matches],0),span([r['reconnect']['pending_at_resume'] for r in matches],0),span([r['reconnect']['resume_drain_ms'] for r in matches]),all(r['reconnect']['completed'] and r['integrity_passed'] for r in matches)])
parts.append('\n## Reconnect\n'+table(['Service','Accepted originals','Pending at resume','Resume drain ms','Complete / exact'],rows))

rows=[]
for r in runs:
    if r['name'].startswith('idle10000'):
        x=r['idle_resources'];rows.append([r['name'],r['service'],x['samples'],span([x['cpu_mean_percent']],1),span([x['memory_max_mib']],1),r['integrity_passed']])
parts.append('\n## Idle 10000\n'+table(['Attempt','Service','Settled samples','CPU %','RAM MiB','Integrity'],rows))

rows=[]
for r in runs:
    if r['name'].startswith('soak-'):
        phase=next(p for p in phases if p['run']==r['name']);x=phase['resources']
        rows.append([r['service'],phase['target_messages_per_second'],phase['planned'],phase['accepted'],phase['skipped'],phase['rejected'],span([phase['delivery']['p95_ms']]),span([phase['delivery']['p99_ms']]),span([x['cpu_mean_percent']],1),span([x['memory_max_mib']],1),r['integrity_passed'],r['load_pass']])
parts.append('\n## 300-second soak\n'+table(['Service','Target/s','Planned','Accepted','Skipped','Rejected','Delivery p95 ms','Delivery p99 ms','CPU %','RAM MiB','Integrity','Strict SLO'],rows))

rows=[]
for r in runs:
    if 'soak_resource_windows' in r:
        w=r['soak_resource_windows'];rows.append([r['service'],span([w['first15']['memory_mean_mib']],1),span([w['last15']['memory_mean_mib']],1),span([w['first15']['cpu_mean_percent']],1),span([w['last15']['cpu_mean_percent']],1)])
parts.append('\n## Soak resource windows (first/last 15 samples, approximately 30 seconds)\n'+table(['Service','Initial mean RAM MiB','Final mean RAM MiB','Initial mean CPU %','Final mean CPU %'],rows))

rows=[]
for storage in ['file','memory']:
    for rate in [10,50,100,5]:
        matches=[p for p in phases if p['run'].startswith('consumerstate-'+storage) and p['target_messages_per_second']==rate]
        if not matches:continue
        rows.append([storage.upper(),rate,span([p['accepted']/p['planned']*100 for p in matches]),span([p['delivery']['p95_ms'] for p in matches]),span([p['delivery']['p99_ms'] for p in matches]),span([p['resources']['cpu_mean_percent'] for p in matches],1),span([p['resources']['memory_max_mib'] for p in matches],1),span([p['skipped'] for p in matches],0)])
parts.append('\n## NATS FILE stream, consumer state control\n'+table(['Consumer','Original/s','Accepted/planned %','Delivery p95 ms','Delivery p99 ms','CPU %','RAM MiB','Skipped'],rows))

rows=[]
for size in [16,64]:
    matches=[f for f in s['files'] if f['run'].startswith(f'files{size}m-')]
    files=[f for r in matches for f in r['files']]
    if not files:continue
    rows.append([f'2 × {size} MiB',span([f['upload_mib_per_second'] for f in files]),span([f['download_mib_per_second'] for f in files]),span([r['elapsed_seconds'] for r in matches]),span([r['resources']['memory_max_mib'] for r in matches],1),span([r['resources']['generator_rss_max_mib'] for r in matches],1),all(r['pass'] for r in matches)])
parts.append('\n## QGramm attachments\n'+table(['Concurrent files','Per-file upload MiB/s','Per-file download MiB/s','Whole flow seconds','Server RAM MiB','Generator RSS MiB','All checks'],rows))

rows=[]
for r in s['runs']:
    rows.append([r.get('source','results')+'/'+r['name'],r.get('comparison_excluded',False),r.get('generator_exit_code'),r.get('integrity_passed'),(r.get('integrity') or {}).get('accepted'),(r.get('integrity') or {}).get('missing'),(r.get('integrity') or {}).get('duplicate'),(r.get('history') or {}).get('checked'),r.get('error') or r.get('setup_failure') or '—'])
parts.append('\n## Every attempt\n'+table(['Raw path','Superseded','Exit','Integrity','Accepted','Missing','Duplicate','History checked','Error'],rows))
(root/'tables.md').write_text(''.join(parts))
selected=[r for r in runs if r.get('integrity')]
totals={k:sum((r.get('integrity') or {}).get(v,0) for r in selected) for k,v in [('accepted','accepted'),('expected_deliveries','expected_deliveries'),('received','received'),('missing','missing'),('duplicates','duplicate')]}
totals['history_complete_runs']=sum(r['integrity_passed'] is True for r in selected);totals['history_complete_accepted']=sum(r['integrity']['accepted'] for r in selected if r['integrity_passed'] is True);totals['runs']=len(s['runs']);totals['selected_messaging_runs']=len(selected);totals['file_runs']=len(s['files']);totals['selected_history_checked']=sum((r.get('history') or {}).get('checked',0) for r in selected)
(root/'selected-totals.json').write_text(json.dumps(totals,indent=2)+'\n')
print(json.dumps(totals))
