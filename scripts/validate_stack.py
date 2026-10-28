import urllib.request, urllib.parse, json, time, concurrent.futures, base64, subprocess, os, argparse
from pathlib import Path
parser=argparse.ArgumentParser(description='Validate a disposable running Compose stack; stops its load balancer after testing graceful drain.')
parser.add_argument('--project',default='adaptive-lb')
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
def get(url,headers=None):
 with urllib.request.urlopen(urllib.request.Request(url,headers=headers or {}),timeout=5) as r:return r.read()
def wait_get(url,headers=None):
 deadline=time.monotonic()+45
 while True:
  try:return get(url,headers)
  except OSError:
   if time.monotonic()>=deadline:raise
   time.sleep(.25)
def control(i,outage=0,latency=None):
 cfg={'latency_ms': [10,30,60][i] if latency is None else latency,'jitter_ms':[2,5,10][i],'capacity':[8,32,64][i],'outage_seconds':outage}
 req=urllib.request.Request(f'http://localhost:{9081+i}/control',data=json.dumps(cfg).encode(),method='PUT',headers={'Authorization':'Bearer '+os.environ.get('SIM_TOKEN','local-experiments-only'),'Content-Type':'application/json'})
 with urllib.request.urlopen(req) as r:assert r.status==204
assert get('http://localhost:9090/ready')==b'ready\n'
with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
 responses=list(executor.map(lambda _:get('http://localhost:8080/example?x=1'),range(100)))
assert all(json.loads(x)['query']=='x=1' for x in responses)
auth={'Authorization':'Basic '+base64.b64encode(('admin:'+os.environ.get('GRAFANA_PASSWORD','local-experiments-only')).encode()).decode()}
dash=json.loads(wait_get('http://localhost:3000/api/dashboards/uid/adaptive-lb',auth))
assert len(dash['dashboard']['panels'])==12
health=json.loads(wait_get('http://localhost:3000/api/datasources/uid/prometheus/health',auth));assert health['status']=='OK',health
for p in dash['dashboard']['panels']:
 for target in p['targets']:
  expr=target['expr'].replace('$__rate_interval','10s')
  result=json.loads(get('http://localhost:9091/api/v1/query?'+urllib.parse.urlencode({'query':expr})))
  assert result['status']=='success',(p['title'],result)
control(0,outage=8)
with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
 list(executor.map(lambda _:get('http://localhost:8080/'),range(100)))
deadline=time.monotonic()+5
while time.monotonic()<deadline:
 if 'lb_backend_available{backend="a"} 0' in get('http://localhost:9090/metrics').decode():break
 time.sleep(.1)
metrics=get('http://localhost:9090/metrics').decode()
assert 'lb_backend_available{backend="a"} 0' in metrics,metrics
assert get('http://localhost:9090/ready')==b'ready\n'
control(0)
time.sleep(4.2)
for _ in range(100):get('http://localhost:8080/')
metrics=get('http://localhost:9090/metrics').decode()
assert 'lb_backend_available{backend="a"} 1' in metrics
assert 'lb_backend_circuit_state{backend="a"} 0' in metrics
for i in range(3):control(i,latency=500)
with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
 pending=executor.submit(get,'http://localhost:8080/drain')
 deadline=time.monotonic()+3
 while time.monotonic()<deadline:
  if 'lb_active_requests 1\n' in get('http://localhost:9090/metrics').decode():break
  time.sleep(.01)
 else:raise AssertionError('request never became active')
 env=dict(os.environ)
 subprocess.run(['docker','compose','-p',args.project,'-f',str(root/'compose.yaml'),'stop','-t','20','lb'],env=env,check=True)
 assert json.loads(pending.result())['path']=='/drain'
 state=json.loads(subprocess.check_output(['docker','inspect',args.project+'-lb-1','--format','{{json .State}}'],env=env))
 assert state['ExitCode']==0,state
for i in range(3):control(i)
print('PASS: proxy requests, dashboard provisioning, datasource health, all PromQL panel expressions, backend outage/failover/recovery, and graceful drain.')
