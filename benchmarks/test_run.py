import importlib.util, pathlib, unittest
spec=importlib.util.spec_from_file_location('runner',pathlib.Path(__file__).with_name('run.py'));runner=importlib.util.module_from_spec(spec);spec.loader.exec_module(runner)
class HarnessTests(unittest.TestCase):
    def test_missing_services_fail_fast(self):
        class Dead:
            def poll(self):return 1
        with self.assertRaisesRegex(RuntimeError,'service exited'):
            runner.wait_ready('http://127.0.0.1:1',[Dead()])
    def test_baselines(self):
        self.assertEqual(len(set(runner.ALGORITHMS)),4)
        self.assertEqual([c['capacity'] for c in runner.BASE],[8,32,64])
if __name__=='__main__': unittest.main()
