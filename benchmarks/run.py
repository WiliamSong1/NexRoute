#!/usr/bin/env python3
"""Isolated native processes per run; k6 open-loop arrivals; randomized blocks."""
import argparse, concurrent.futures, hashlib, json, os, pathlib, platform, random, shutil, signal, subprocess, time, urllib.request
ROOT = pathlib.Path(__file__).resolve().parents[1]
ALGORITHMS = ['round_robin', 'least_connections', 'weighted_round_robin', 'adaptive']
BASE = [dict(latency_ms=10, jitter_ms=2, capacity=8), dict(latency_ms=30, jitter_ms=5, capacity=32), dict(latency_ms=60, jitter_ms=10, capacity=64)]

def wait_ready(url, processes):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if any(p.poll() is not None for p in processes):
            raise RuntimeError('service exited; inspect run logs')
        try:
            with urllib.request.urlopen(url, timeout=.5) as res:
                if res.status == 200:
                    return
        except OSError:
            time.sleep(.1)
    raise RuntimeError('readiness deadline exceeded')

def configure(index, cfg, token):
    req = urllib.request.Request(f'http://127.0.0.1:{9081+index}/control', data=json.dumps(cfg).encode(), method='PUT', headers={'Authorization': 'Bearer '+token, 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=2) as res:
        if res.status != 204:
            raise RuntimeError('control failed')

def run_one(args, algorithm, scenario, repetition, ordinal):
    directory = args.output / f'{scenario}-{repetition:02d}-{algorithm}'
    directory.mkdir(parents=True, exist_ok=False)
    processes, logs = [], []
    token = 'isolated-local-benchmark'
    env = dict(os.environ, SIM_TOKEN=token)
    metadata = dict(algorithm=algorithm, scenario=scenario, repetition=repetition, ordinal=ordinal,
                    seed=args.seed+repetition, rate=args.rate, duration=args.duration, warmup=args.warmup,
                    platform=platform.platform(), cpu_count=os.cpu_count(), k6_version=args.k6_version,
                    go_version=args.go_version, source_sha256=args.source_sha256, config=json.loads((ROOT/'configs/local.json').read_text()), started_utc=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()))
    def spawn(argv, name, child_env=env):
        log = open(directory / (name+'.log'), 'w'); logs.append(log)
        proc = subprocess.Popen(argv, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, env=child_env)
        processes.append(proc); return proc
    try:
        for i, cfg in enumerate(BASE):
            spawn([str(ROOT/'bin/backend'), '-id', chr(97+i), '-listen', f'127.0.0.1:{8081+i}', '-admin', f'127.0.0.1:{9081+i}', '-latency', str(cfg['latency_ms']), '-jitter', str(cfg['jitter_ms']), '-capacity', str(cfg['capacity']), '-seed', str(args.seed+repetition*10+i)], 'backend-'+chr(97+i))
        target = 'http://127.0.0.1:8081/' if algorithm == 'direct' else 'http://127.0.0.1:8080/'
        if algorithm != 'direct':
            spawn([str(ROOT/'bin/lb'), '-config', 'configs/local.json'], 'lb', dict(env, LB_ALGORITHM=algorithm))
        wait_ready('http://127.0.0.1:8081/health' if algorithm == 'direct' else 'http://127.0.0.1:9090/ready', processes)
        for i in range(3): wait_ready(f'http://127.0.0.1:{8081+i}/health', processes)
        load_env = dict(env, TARGET=target, RATE=str(args.rate), MAX_VUS=str(args.max_vus))
        def load(seconds, summary, scenario_name):
            loadenv = dict(load_env, DURATION=str(seconds), SUMMARY=str(directory/summary), SCENARIO=scenario_name)
            log = open(directory/(summary+'.log'), 'w'); logs.append(log)
            # A finite external timeout prevents a stuck load generator holding services open.
            subprocess.run([args.k6, 'run', '--quiet', str(ROOT/'benchmarks/load.js')], env=loadenv, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, timeout=seconds+45, check=True)
        load(args.warmup, 'warmup.json', 'heterogeneous')
        if algorithm != 'direct':
            with urllib.request.urlopen('http://127.0.0.1:9090/metrics') as r: (directory/'metrics-before.prom').write_bytes(r.read())
        def fault():
            time.sleep(args.duration/3)
            cfg = dict(BASE[0])
            if scenario == 'latency_spike': cfg.update(spike_ms=400, spike_seconds=max(1,int(args.duration/3)))
            elif scenario == 'outage': cfg.update(outage_seconds=max(1,int(args.duration/3)))
            elif scenario == 'errors': cfg.update(error_probability=.4)
            else: return
            configure(0, cfg, token)
            time.sleep(args.duration/3)
            configure(0, BASE[0], token)
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(fault)
            load(args.duration, 'summary.json', scenario)
            future.result()  # Fail the run if fault injection failed.
        if algorithm != 'direct':
            with urllib.request.urlopen('http://127.0.0.1:9090/metrics') as r: (directory/'metrics-after.prom').write_bytes(r.read())
        metadata['completed'] = True
    except BaseException as e:
        metadata.update(completed=False, error=str(e))
        raise
    finally:
        (directory/'metadata.json').write_text(json.dumps(metadata, indent=2)+'\n')
        for p in reversed(processes):
            if p.poll() is None:
                p.send_signal(signal.SIGTERM)
                try: p.wait(timeout=20)
                except subprocess.TimeoutExpired: p.kill(); p.wait()
        for log in logs: log.close()

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=pathlib.Path, default=ROOT/'benchmarks/results')
    p.add_argument('--algorithms', nargs='+', choices=ALGORITHMS+['direct'], default=ALGORITHMS)
    p.add_argument('--scenarios', nargs='+', choices=['heterogeneous','latency_spike','outage','errors','traffic_spike'], default=['heterogeneous','latency_spike','outage','errors','traffic_spike'])
    p.add_argument('--repetitions', type=int, default=5); p.add_argument('--duration', type=int, default=30)
    p.add_argument('--warmup', type=int, default=10); p.add_argument('--rate', type=int, default=900)
    p.add_argument('--max-vus', type=int, default=1000);p.add_argument('--seed', type=int, default=42)
    p.add_argument('--k6', default='k6');args=p.parse_args()
    if min(args.repetitions,args.duration,args.warmup,args.rate,args.max_vus)<1: p.error('all numeric limits must be positive')
    if args.duration%3: p.error('duration must be divisible by 3 for scenario phases')
    for binary in ['lb','backend']:
        if not (ROOT/'bin'/binary).exists(): p.error('run make build first')
    args.k6=shutil.which(args.k6) or args.k6
    args.k6_version=subprocess.check_output([args.k6,'version'],text=True).strip()
    args.go_version=subprocess.check_output(['go','version'],text=True).strip()
    digest=hashlib.sha256()
    for path in sorted(list((ROOT/'internal').rglob('*.go'))+list((ROOT/'cmd').rglob('*.go'))+[ROOT/'benchmarks/load.js',ROOT/'configs/local.json']):
        digest.update(str(path.relative_to(ROOT)).encode());digest.update(path.read_bytes())
    args.source_sha256=digest.hexdigest()
    args.output=args.output.resolve();args.output.mkdir(parents=True,exist_ok=True)
    rng=random.Random(args.seed);ordinal=0
    for repetition in range(args.repetitions):
        scenarios=list(args.scenarios);rng.shuffle(scenarios)
        for scenario in scenarios:
            algorithms=list(args.algorithms);rng.shuffle(algorithms)
            for algorithm in algorithms:
                ordinal+=1;print(f'{ordinal}: {scenario} / {algorithm} / repetition {repetition}',flush=True)
                run_one(args,algorithm,scenario,repetition,ordinal)
if __name__=='__main__': main()
