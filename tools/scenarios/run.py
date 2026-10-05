#!/usr/bin/env python3
"""Disposable, loopback-only real services for the shared scenario generator."""
import argparse, hashlib, json, os, pathlib, platform, secrets, subprocess
import tempfile, threading, time, uuid, urllib.request, urllib.error

ROOT = pathlib.Path(__file__).resolve().parents[2]

def command(*args):
    return subprocess.check_output(args, text=True, cwd=ROOT).strip()

def wait_ready(url, timeout=30):
    started = time.monotonic()
    while time.monotonic() - started < timeout:
        try:
            urllib.request.urlopen(url, timeout=min(1, max(.01, timeout - (time.monotonic() - started)))).close()
            return time.monotonic() - started
        except urllib.error.HTTPError as error:
            error.close()
            time.sleep(.1)
        except Exception:
            time.sleep(.1)
    raise TimeoutError('server readiness timeout')

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--service', choices=['qgramm','nats','centrifugo'], required=True)
    p.add_argument('--image', required=True)
    p.add_argument('--generator', required=True)
    p.add_argument('--env-generator', help='existing qgramm-bench binary, synthetic fixture init only')
    p.add_argument('--out', required=True)
    p.add_argument('--users', type=int, default=2000)
    p.add_argument('--chats', type=int, default=100)
    p.add_argument('--fanout', type=int, default=1)
    p.add_argument('--payload-bytes', type=int, default=256)
    p.add_argument('--steps', default='500:20s,1500:20s,3000:20s,100:20s')
    p.add_argument('--response-mode', choices=['full','minimal'], default='full')
    p.add_argument('--reconnect', type=int, default=0)
    p.add_argument('--offline', default='0s')
    p.add_argument('--idle', default='10s')
    p.add_argument('--timeout', type=float, default=900)
    p.add_argument('--file-bytes', type=int, default=0)
    p.add_argument('--file-count', type=int, default=2)
    p.add_argument('--nats-consumer-memory', action='store_true', help='NATS sensitivity only: keep consumer state in RAM; messages stay FILE')
    a = p.parse_args()
    if a.nats_consumer_memory and a.service != 'nats': p.error('--nats-consumer-memory requires --service nats')
    if a.service == 'qgramm' and not a.env_generator: p.error('qgramm requires --env-generator')
    out = pathlib.Path(a.out).resolve()
    if out.exists(): p.error('refusing to overwrite evidence')
    name = 'qgramm-scenario-'+uuid.uuid4().hex[:12]
    stop = threading.Event(); samples = []; thread = None; process = None
    try:
        with tempfile.TemporaryDirectory(prefix=name) as directory:
            temp = pathlib.Path(directory); config = temp/'config'; result=temp/'result.json'; phase=temp/'phase'; env_file=temp/'fixture.env'
            secret = secrets.token_hex(32)
            volume = a.service != 'centrifugo'
            if volume: command('docker','volume','create',name)
            common=['docker','run','-d','--name',name,'--cpus','4','--memory','8g','--ulimit','nofile=65536:65536']
            if a.service == 'qgramm':
                command(str(pathlib.Path(a.env_generator).resolve()),'-init','-env',str(env_file))
                container_env=temp/'container.env'
                container_env.write_text('\n'.join(line for line in env_file.read_text().splitlines() if not line.startswith('QGRAMM_BENCH_SIGNING_KEY='))+'\n');container_env.chmod(0o600)
                config.write_text('[server]\nlisten="0.0.0.0:8080"\ntrusted_proxy=true\n[storage]\npath="/data/qgramm.db"\nfiles="/data/files"\n[capacity]\nexpected_concurrent_users=10000\nmax_connections=12000\n[features]\ngroups=true\nfiles='+str(a.file_bytes>0).lower()+'\n'+('[policy]\nmax_file_bytes=134217728\nmax_chunk_bytes=2097152\nmax_storage_bytes=1073741824\n' if a.file_bytes else ''))
                common+=['--env-file',str(container_env),'-p','127.0.0.1::8080','-v',name+':/data','--mount','type=bind,src='+str(config)+',dst=/app/scenario.toml,readonly',a.image,'-config','/app/scenario.toml']
                port='8080/tcp';scheme='http://';suffix=''
            elif a.service == 'nats':
                config.write_text('port:4222\nhttp:8222\nwebsocket { port:8080; no_tls:true }\nmax_connections:12000\nauthorization { token:"'+secret+'" }\njetstream { store_dir:"/data";sync_interval:always }\n')
                common+=['-p','127.0.0.1::8080','-p','127.0.0.1::8222','-v',name+':/data','--mount','type=bind,src='+str(config)+',dst=/config/nats.conf,readonly',a.image,'-c','/config/nats.conf']
                port='8080/tcp';scheme='ws://';suffix=''
            else:
                config.write_text(json.dumps({'health':{'enabled':True},'client':{'token':{'hmac_secret_key':secret},'history_max_publication_limit':1000000},'channel':{'without_namespace':{'allow_subscribe_for_client':True,'allow_publish_for_client':True,'allow_history_for_client':True,'force_recovery':True,'history_size':1000000,'history_ttl':'900s'}},'engine':{'type':'memory'}}))
                common+=['-p','127.0.0.1::8000','--mount','type=bind,src='+str(config)+',dst=/config.json,readonly',a.image,'-c','/config.json']
                port='8000/tcp';scheme='ws://';suffix='/connection/websocket'
            command(*common);address=command('docker','port',name,port)
            health_address=command('docker','port',name,'8222/tcp') if a.service=='nats' else address
            health_path={'qgramm':'/readyz','nats':'/healthz?js-enabled-only=true','centrifugo':'/health'}[a.service]
            try:
                readiness_seconds=wait_ready('http://'+health_address+health_path)
            except TimeoutError:
                out.parent.mkdir(parents=True,exist_ok=True)
                out.write_text(json.dumps({'service':a.service,'setup_failure':'server readiness timeout','comparison_excluded':True,'integrity_passed':False},indent=2)+'\n')
                raise RuntimeError('temporary '+a.service+' readiness failed before workload')
            args=[str(pathlib.Path(a.generator).resolve()),'-service',a.service,'-url',scheme+address+suffix,'-users',str(a.users),'-chats',str(a.chats),'-fanout',str(a.fanout),'-payload-bytes',str(a.payload_bytes),'-steps',a.steps,'-idle',a.idle,'-response-mode',a.response_mode,'-reconnect',str(a.reconnect),'-offline',a.offline,'-out',str(result),'-phase-file',str(phase),'-file-bytes',str(a.file_bytes),'-file-count',str(a.file_count)]
            if a.service=='qgramm':args+=['-env-file',str(env_file)]
            if a.nats_consumer_memory:args+=['-nats-consumer-memory']
            start=time.monotonic()
            with (temp/'generator.log').open('w') as log:
                process=subprocess.Popen(args,cwd=ROOT,env=dict(os.environ,BENCH_SECRET=secret),stdout=log,stderr=subprocess.STDOUT)
                def sample():
                    while not stop.is_set():
                        try:
                            d=json.loads(command('docker','stats','--no-stream','--format','{{json .}}',name))
                            g=command('ps','-p',str(process.pid),'-o','%cpu=,rss=').split()
                            samples.append({'elapsed_seconds':round(time.monotonic()-start,3),'phase':phase.read_text() if phase.exists() else 'setup','cpu':d['CPUPerc'],'memory':d['MemUsage'],'network_io':d['NetIO'],'block_io':d['BlockIO'],'generator_cpu_lifetime_percent':float(g[0]) if g else None,'generator_rss_kib':int(g[1]) if len(g)>1 else None})
                        except Exception:
                            if process.poll() is not None:break
                        stop.wait(1)
                thread=threading.Thread(target=sample,daemon=True);thread.start()
                try:process.wait(timeout=a.timeout)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    try:process.wait(timeout=10)
                    except subprocess.TimeoutExpired:process.kill();process.wait()
            stop.set();thread.join(10)
            evidence=json.loads(result.read_text()) if result.exists() else {'error':'generator ended without evidence','integrity_passed':False}
            inspect=json.loads(command('docker','inspect',name))[0]
            image=json.loads(command('docker','image','inspect',a.image))[0]
            evidence['resource_samples']=samples
            evidence['generator_exit_code']=process.returncode
            evidence['readiness_seconds']=round(readiness_seconds,3)
            evidence['environment']={'host_os':platform.platform(),'host_cpu':command('sysctl','-n','machdep.cpu.brand_string') if platform.system()=='Darwin' else platform.processor(),'host_logical_cpus':os.cpu_count(),'server_kernel':command('docker','exec',name,'uname','-a') if inspect['State']['Running'] else 'container stopped','cpu_limit':4,'ram_limit_gib':8,'image':a.image,'image_id':inspect['Image'],'image_labels':image['Config'].get('Labels'),'generator_sha256':hashlib.sha256(pathlib.Path(a.generator).read_bytes()).hexdigest(),'tooling_commit':command('git','rev-parse','HEAD'),'container_oom':inspect['State']['OOMKilled'],'isolation':'shared Docker Desktop VM, generator host, noTLS, loopback only','stats':'docker no-stream+1s (~2s); CPU100%=onecore; generator psCPU lifetime mean, not intervalCPU'}
            evidence['server_configuration']=config.read_text().replace(secret,'<synthetic-secret>')
            evidence['contract']={'durability':{'qgramm':'SQLite WAL FULL durable operation/event transaction','nats':'JetStream FILE messages R1 sync_interval=always; explicit-ACK consumer state '+('MEMORY' if a.nats_consumer_memory else 'FILE')+' per recipient','centrifugo':'Memory history cache1M/channel TTL900s; no restart durability'}[a.service],'crypto':'client/container and container/recipient HPKE, storage AEAD' if a.service=='qgramm' else 'none; isolated loopback payload','auth':'per-request Ed25519 JWT and chat/device ACL' if a.service=='qgramm' else 'shared connection token' if a.service=='nats' else 'distinct HS256 connectionJWT, permissive benchmark channels','transport':'HTTP send/WebSocket receive' if a.service=='qgramm' else 'WebSocket send/receive'}
            if a.service=='nats':
                evidence['contract']['stream_storage']='FILE'
                evidence['contract']['consumer_storage']='MEMORY' if a.nats_consumer_memory else 'FILE'
                evidence['contract']['consumer_restart']='not tested; MEMORY state must not be treated as restart durable'
            out.parent.mkdir(parents=True,exist_ok=True);out.write_text(json.dumps(evidence,indent=2)+'\n')
            if process.returncode:raise RuntimeError('scenario failed; saved evidence '+str(out))
            print(json.dumps({'out':str(out),'exit':process.returncode}),flush=True)
    finally:
        stop.set()
        if process and process.poll() is None:process.kill();process.wait()
        if thread:thread.join(10)
        subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if a.service!='centrifugo':subprocess.run(['docker','volume','rm',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

if __name__=='__main__':main()
