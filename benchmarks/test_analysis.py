import importlib.util
import pathlib
import tempfile
import unittest
import json

available = all(importlib.util.find_spec(x) is not None for x in ['numpy','pandas','matplotlib'])
if available:
    spec=importlib.util.spec_from_file_location('analysis',pathlib.Path(__file__).with_name('analyze.py'))
    analysis=importlib.util.module_from_spec(spec);spec.loader.exec_module(analysis)

@unittest.skipUnless(available, 'install benchmark analysis requirements')
class AnalysisTests(unittest.TestCase):
    def test_bootstrap_is_reproducible(self):
        self.assertEqual(analysis.bootstrap_mean([1,2,3]),analysis.bootstrap_mean([1,2,3]))
        mean,low,high=analysis.bootstrap_mean([4,4,4])
        self.assertEqual((mean,low,high),(4,4,4))

    def test_incomplete_runs_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);run=root/'failed';run.mkdir()
            (run/'metadata.json').write_text(json.dumps({'completed':False}))
            with self.assertRaisesRegex(ValueError,'incomplete run'):
                analysis.read_runs(root)

    def test_summary_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);run=root/'ok';run.mkdir()
            (run/'metadata.json').write_text(json.dumps({'completed':True,'algorithm':'adaptive','scenario':'test','repetition':0}))
            data={'metrics':{'http_reqs':{'values':{'count':100,'rate':10}},'http_req_duration':{'values':{'med':3,'p(95)':9,'p(99)':20,'avg':4}},'http_req_failed':{'values':{'rate':.2}}}}
            (run/'summary.json').write_text(json.dumps(data))
            row=analysis.read_runs(root).iloc[0]
            self.assertEqual(row.success_rps,8)
            self.assertEqual(row.p99_ms,20)
            self.assertEqual(row.dropped,0)
if __name__=='__main__':unittest.main()
