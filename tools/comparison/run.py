#!/usr/bin/env python3
"""Real isolated baseline services; owns only UUID-named containers/volumes."""
import argparse, hashlib, json, os, pathlib, platform, secrets, subprocess, tempfile, threading, time, uuid
ROOT = pathlib.Path(__file__).resolve().parents[2]
HERE = pathlib.Path(__file__).resolve().parent
IMAGES = {'nats': 'nats:2.11.3-alpine', 'centrifugo': 'centrifugo/centrifugo:v6.2.3'}

def cmd(*args, capture=True, cwd=ROOT):
    return subprocess.run(args, cwd=cwd, check=True, text=True, stdout=subprocess.PIPE if capture else None).stdout

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--service', choices=IMAGES, required=True)
    p.add_argument('--out', required=True)
    p.add_argument('--image', help='existing source-built image; skip DockerHub pull')
    p.add_argument('--users', type=int, default=10000)
    p.add_argument('--timeout', type=float, default=300, help='overall generator timeout seconds; increase explicitly for long runs')
    p.add_argument('--duration',default='30s');p.add_argument('--rate',type=int,default=100)
    p.add_argument('--burst',default='5s');p.add_argument('--burst-rate',type=int,default=1000)
    p.add_argument('--nats-sync',choices=['default','always'],default='default')
    args=p.parse_args()
    if args.timeout <= 0: p.error('--timeout must be positive')
    name='qgramm-comparison-'+uuid.uuid4().hex[:12]; image=args.image or IMAGES[args.service]
    samples=[]; stop=threading.Event();thread=None
    try:
        if not args.image: cmd('docker','pull',image)
        with tempfile.TemporaryDirectory(prefix=name) as directory:
            temp=pathlib.Path(directory); binary=temp/'generator';result=temp/'result.json';config=temp/'config'
            cmd('go','build','-mod=readonly','-o',str(binary),'.',cwd=HERE)
            secret=secrets.token_hex(32)
            if args.service=='nats':
                config.write_text('port: 4222\nwebsocket { port: 8080; no_tls: true }\nmax_connections: 12000\nauthorization { token: "'+secret+'" }\njetstream { store_dir: "/data"'+ ('\nsync_interval: always' if args.nats_sync=='always' else '')+' }\n')
                cmd('docker','volume','create',name)
                cmd('docker','run','-d','--name',name,'--cpus','4','--memory','8g','--ulimit','nofile=65536:65536','-p','127.0.0.1::8080','--mount','type=bind,src='+str(config)+',dst=/config/nats.conf,readonly','-v',name+':/data',image,'-c','/config/nats.conf')
                port='8080/tcp';scheme='ws://';suffix=''
            else:
                config.write_text(json.dumps({'client':{'token':{'hmac_secret_key':secret}},'channel':{'without_namespace':{'allow_subscribe_for_client':True,'allow_publish_for_client':True,'history_size':10000,'history_ttl':'60s'}},'engine':{'type':'memory'}}))
                cmd('docker','run','-d','--name',name,'--cpus','4','--memory','8g','--ulimit','nofile=65536:65536','-p','127.0.0.1::8000','--mount','type=bind,src='+str(config)+',dst=/centrifugo/config.json,readonly',image,'centrifugo','-c','/centrifugo/config.json')
                port='8000/tcp';scheme='ws://';suffix='/connection/websocket'
            address=cmd('docker','port',name,port).strip(); start=time.monotonic()
            def sample():
                while not stop.is_set():
                    try:
                        r=json.loads(cmd('docker','stats','--no-stream','--format','{{json .}}',name))
                        samples.append({'elapsed_seconds':round(time.monotonic()-start,2),'cpu':r['CPUPerc'],'memory':r['MemUsage']})
                    except Exception: break
                    stop.wait(1)
            thread=threading.Thread(target=sample,daemon=True);thread.start();time.sleep(3)
            env=dict(os.environ,BENCH_SECRET=secret)
            command=[str(binary),'-service',args.service,'-url',scheme+address+suffix,'-users',str(args.users),'-duration',args.duration,'-rate',str(args.rate),'-burst',args.burst,'-burst-rate',str(args.burst_rate),'-out',str(result)]
            run = subprocess.run(command,cwd=HERE,env=env,check=False,timeout=args.timeout)
            if not result.exists(): run.check_returncode()
            stop.set();thread.join(10)
            evidence=json.loads(result.read_text()); inspect=json.loads(cmd('docker','inspect',name))[0]
            try: server_kernel = cmd('docker','exec',name,'uname','-a').strip()
            except subprocess.CalledProcessError: server_kernel = 'unavailable: container not running'
            evidence['container_state'] = {'status': inspect['State']['Status'], 'oom_killed': inspect['State']['OOMKilled']}
            evidence['generator_exit_code'] = run.returncode
            evidence['environment']={'date_utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'host_os':platform.platform(),'host_logical_cpus':os.cpu_count(),'host_cpu':cmd('sysctl','-n','machdep.cpu.brand_string').strip() if platform.system()=='Darwin' else platform.processor(),'generator_go':cmd('go','version').strip(),'image_tag':image,'image_id':inspect['Image'],'repo_digests':json.loads(cmd('docker','image','inspect',image))[0].get('RepoDigests',[]),'cpu_limit':4,'ram_limit_gib':8,'server_kernel':server_kernel,'storage':'Docker named volume, VM virtual disk' if args.service=='nats' else 'in-memory bounded history, no volume','isolation':'shared Docker Desktop VM; generator on host; loopback only; sequential baseline runs','source_commit':cmd('git','rev-parse','HEAD').strip(),'generator_sha256':hashlib.sha256((HERE/'main.go').read_bytes()).hexdigest()}
            evidence['resource_samples']=samples;evidence['resource_sampling']='docker stats no-stream + 1s wait (~2s), sampled maxima; startup/idle/connections/load included'
            evidence['contract']={'nats_sync_interval':args.nats_sync if args.service=='nats' else None,'auth':'shared NATS token' if args.service=='nats' else 'distinct-user HS256 connection JWT; any authenticated user may subscribe/publish bench channel','durability':'JetStream file stream, replicas=1; default sync_interval=2m; ACK does not imply fsync' if args.service=='nats' and args.nats_sync=='default' else 'JetStream file stream replicas=1 sync_interval=always' if args.service=='nats' else 'in-memory history_size=10000 history_ttl=60s; ACK is publication accepted, not durable chat','application_crypto':'none; plaintext transport/application data in isolated loopback fixture','active_participants':2,'idle_connections':args.users-2}
            output=pathlib.Path(args.out).resolve();output.parent.mkdir(parents=True,exist_ok=True);output.write_text(json.dumps(evidence,indent=2)+'\n')
            run.check_returncode()
    finally:
        stop.set()
        if thread:thread.join(10)
        subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if args.service=='nats':subprocess.run(['docker','volume','rm',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
if __name__=='__main__':main()
