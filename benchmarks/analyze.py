#!/usr/bin/env python3
"""Summarize independent runs and paired bootstrap uncertainty; never pool quantiles."""
import argparse, json, pathlib
import numpy as np
import pandas as pd
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

def read_runs(root):
    rows=[]
    for path in sorted(root.glob('*/metadata.json')):
        meta=json.loads(path.read_text())
        if not meta.get('completed'):
            raise ValueError(f'incomplete run: {path}')
        data=json.loads((path.parent/'summary.json').read_text())['metrics']
        values=lambda key:data.get(key,{}).get('values',{})
        latency=values('http_req_duration')
        rows.append(dict(**meta,rps=values('http_reqs')['rate'], requests=values('http_reqs')['count'],
                         p50_ms=latency['med'],p95_ms=latency['p(95)'],p99_ms=latency['p(99)'],mean_ms=latency['avg'],
                         error_rate=values('http_req_failed')['rate'], success_rps=values('http_reqs')['rate']*(1-values('http_req_failed')['rate']), dropped=values('dropped_iterations').get('count',0)))
    if not rows: raise ValueError('no completed runs found')
    return pd.DataFrame(rows)

def bootstrap_mean(xs,seed=42):
    xs=np.asarray(xs,dtype=float)
    if len(xs)<2: return float(xs.mean()),float('nan'),float('nan')
    rng=np.random.default_rng(seed)
    means=rng.choice(xs,size=(10000,len(xs)),replace=True).mean(axis=1)
    return float(xs.mean()),float(np.quantile(means,.025)),float(np.quantile(means,.975))

def main():
    parser=argparse.ArgumentParser();parser.add_argument('root',type=pathlib.Path);args=parser.parse_args();df=read_runs(args.root)
    df.to_csv(args.root/'runs.csv',index=False)
    keys=['rps','p50_ms','p95_ms','p99_ms','mean_ms','error_rate','success_rps','dropped']
    summary=[]
    for (scenario,algorithm),group in df.groupby(['scenario','algorithm']):
        for metric in keys:
            mean,lo,hi=bootstrap_mean(group[metric]);summary.append(dict(scenario=scenario,algorithm=algorithm,metric=metric,n=len(group),mean=mean,ci95_low=lo,ci95_high=hi))
    pd.DataFrame(summary).to_csv(args.root/'summary.csv',index=False)
    comparisons=[]
    for scenario,g in df.groupby('scenario'):
        adaptive=g[g.algorithm=='adaptive'].set_index('repetition')
        for baseline in ['round_robin','least_connections','weighted_round_robin']:
            base=g[g.algorithm==baseline].set_index('repetition')
            shared=adaptive.index.intersection(base.index)
            if len(shared)<2: continue
            for metric in ['p95_ms','p99_ms','rps','error_rate']:
                # Absolute paired difference avoids unstable ratios near zero.
                deltas=adaptive.loc[shared,metric]-base.loc[shared,metric]
                mean,lo,hi=bootstrap_mean(deltas)
                comparisons.append(dict(scenario=scenario,baseline=baseline,metric=metric,n=len(shared),adaptive_minus_baseline=mean,ci95_low=lo,ci95_high=hi))
    pd.DataFrame(comparisons).to_csv(args.root/'paired_comparisons.csv',index=False)
    scenarios=list(df.scenario.unique());fig,axes=plt.subplots(len(scenarios),3,figsize=(15,4*len(scenarios)),squeeze=False)
    for row,scenario in enumerate(scenarios):
        g=df[df.scenario==scenario]
        for col,(metric,label) in enumerate([('p99_ms','P99 latency (ms)'),('rps','Completed requests/s'),('error_rate','Error fraction')]):
            ax=axes[row,col]
            for i,(algorithm,group) in enumerate(g.groupby('algorithm')):
                mean,lo,hi=bootstrap_mean(group[metric]);ax.scatter([i]*len(group),group[metric],alpha=.4)
                if np.isfinite(lo): ax.errorbar(i,mean,yerr=[[max(0,mean-lo)],[max(0,hi-mean)]],fmt='o',capsize=4,color='black')
            names=sorted(g.algorithm.unique());ax.set_xticks(range(len(names)),[x.replace('_','\n') for x in names]);ax.set_title(scenario);ax.set_ylabel(label);ax.set_ylim(bottom=0);ax.grid(axis='y',alpha=.2)
            if metric=='error_rate' and g[metric].max()==0: ax.set_ylim(0,.01)
    fig.tight_layout();fig.savefig(args.root/'comparison.png',dpi=160);plt.close(fig)
    notes=['# Benchmark interpretation','',f'{len(df)} completed runs. Dots are independent run summaries; bars are 95% bootstrap intervals for their means.', '', 'Quantiles are computed per run; these are means of run quantiles, not pooled latency quantiles. Comparisons pair the same repetition and scenario. Lower latency is only useful alongside error rate and dropped arrivals. RPS includes failed HTTP requests. Failed runs must be investigated, not removed.']
    if df.groupby(['scenario','algorithm']).size().min()<5:notes += ['', '**Exploratory only:** fewer than five independent repetitions in at least one group. Do not claim statistical superiority from this smoke run.']
    if df.dropped.sum()>0:notes += ['', '**Dropped arrivals detected.** Check generator CPU/VUs and server overload before comparing throughput.']
    notes += ['', 'Bootstrap intervals describe observed run-to-run variation, not host/environment uncertainty. Multiple comparisons are not corrected; use a predeclared primary endpoint and independent confirmation before publishing a superiority claim.']
    (args.root/'REPORT.md').write_text('\n'.join(notes)+'\n')
    print(args.root/'REPORT.md')
if __name__=='__main__': main()
