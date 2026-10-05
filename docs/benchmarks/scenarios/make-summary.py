import json,pathlib,re,statistics
root=pathlib.Path(__file__).parent
out=root/'results'

def mib(value):
    raw=value.split(' / ')[0].strip()
    m=re.fullmatch(r'([0-9.]+)\s*([A-Za-z]+)',raw)
    if not m: return 0.0
    return float(m[1])*{'B':1/1048576,'kB':1000/1048576,'MB':1000000/1048576,'GB':1000000000/1048576,'KiB':1/1024,'MiB':1,'GiB':1024}.get(m[2],0)

def resources(r,phase=None,tail=None):
    rows=[s for s in r.get('resource_samples',[]) if phase is None or s['phase']==phase]
    if tail: rows=rows[-tail:]
    cpu=[float(s['cpu'].rstrip('%')) for s in rows]
    return {'samples':len(rows),'cpu_mean_percent':statistics.mean(cpu) if cpu else None,'cpu_max_percent':max(cpu) if cpu else None,'memory_max_mib':max([mib(s['memory']) for s in rows],default=None),'memory_mean_mib':statistics.mean(mib(s['memory']) for s in rows) if rows else None,'generator_cpu_lifetime_max_percent':max([s['generator_cpu_lifetime_percent'] or 0 for s in rows],default=None),'generator_rss_max_mib':max([s['generator_rss_kib']/1024 for s in rows if s['generator_rss_kib'] is not None],default=None)}

rows=[];runs=[];files=[]
for p in sorted(list(out.glob('*.json'))+list((root/'corrected').glob('*.json'))):
    r=json.loads(p.read_text());name=p.stem; source=p.parent.name; excluded=source=='results' and name.startswith('payload4k-')
    if 'resource_samples' not in r:
        runs.append({'name':name,'source':source,'comparison_excluded':True,'excluded':True,'setup_failure':r.get('setup_failure'),'integrity_passed':False,'integrity':None,'history':None});continue
    run={'name':name,'source':source,'comparison_excluded':excluded,'nats_consumer_storage':r.get('nats_consumer_storage'),'service':r.get('service', 'qgramm' if 'files' in r else None),'integrity_passed':r.get('integrity_passed'),'load_pass':r.get('load_pass'),'generator_exit_code':r.get('generator_exit_code'),'error':r.get('error'),'resources':resources(r),'idle_resources':resources(r,'idle',tail=5),'history':r.get('history'),'integrity':r.get('integrity'),'reconnect':r.get('reconnect'),'campaign':r.get('campaign')}
    if name.startswith('soak-'):
        active=[x for x in r['resource_samples'] if x['phase']=='step-0']
        run['soak_resource_windows']={'first15':resources({'resource_samples':active[:15]}),'last15':resources({'resource_samples':active[-15:]})}
    runs.append(run)
    for phase in (r.get('phases') or []):
        rows.append(dict(phase,run=name,service=r['service'],users=r['users'],chats=r['chats'],fanout=r['fanout'],payload_bytes=r['payload_bytes'],integrity_passed=r['integrity_passed'],resources=resources(r,phase['name']),source=source,comparison_excluded=excluded))
    if 'files' in r:
        checks=['corrupt_chunk_rejected','exact_chunk_retry','complete_retry','resume_status_verified','unpublished_recipient_denied','download_verified']
        files.append({'run':name,'files':r['files'],'elapsed_seconds':r['elapsed_seconds'],'total_plain_bytes':r['total_plain_bytes'],'pass':r.get('generator_exit_code')==0 and r['websocket_errors']==0 and all(f[k] for f in r['files'] for k in checks),'resources':resources(r)})
summary={'phase_rows':rows,'runs':runs,'files':files,'totals':{'runs':len(runs),'messaging_accepted':sum((r.get('integrity') or {}).get('accepted',0) for r in runs),'expected_deliveries':sum((r.get('integrity') or {}).get('expected_deliveries',0) for r in runs),'unique_deliveries':sum((r.get('integrity') or {}).get('received',0) for r in runs),'missing':sum((r.get('integrity') or {}).get('missing',0) for r in runs),'duplicates':sum((r.get('integrity') or {}).get('duplicate',0) for r in runs),'history_checked':sum((r.get('history') or {}).get('checked',0) for r in runs)}}
(root/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
print(json.dumps(summary['totals']))
for r in runs:print(r['name'],r['integrity_passed'],r.get('error'))
