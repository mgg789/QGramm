import json
import pathlib
import tempfile
import unittest
import sys

from timeline import summarize
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from benchmark import checkpoint_metadata


class ClockAlignmentTest(unittest.TestCase):
    def fixture(self, folder, drift=0, uncertainty=1_000_000):
        offset = 7_000_000_000  # host is seven seconds ahead of the container
        result = {'diagnostic_phase_markers': [{'phase':'idle','at_unix_ns':offset+100},
                                              {'phase':'history','at_unix_ns':offset+300}],
                  'resource_samples': [], 'diagnostic_clock_pairs': {
                      'before':[{'offset_host_minus_container_ns':offset,'uncertainty_ns':uncertainty}],
                      'after':[{'offset_host_minus_container_ns':offset+drift,'uncertainty_ns':uncertainty}]},
                  'environment': {'server_source_commit':'test','server_source_note':'private annotation'},
                  'accepted':10,'delivered':10,'history_messages':10}
        histogram_keys = ('queue_wait_jobs','writer_service_transactions','transaction_body_calls','commit_transactions',
                          'query_lifetime_calls','projection_calls','ws_write_calls','checkpoint_calls')
        rows = []
        for stamp, count in [(50,1),(100,2),(200,12),(400,1000)]:
            diagnostic = {'slow_records': [], 'gc_recent_pauses': [], 'sql_read_calls':count,
                          'total_alloc_bytes':count*100,'mallocs':count,'gc_cycles':0,
                          'gc_pause_total_ns':0,'wal_bytes':4096}
            diagnostic.update({key:[count]+[0]*9 for key in histogram_keys})
            rows.append({'at_unix_ns':stamp,'forced_gc':False,'diagnostics':diagnostic})
        rp, tp = folder/'result.json', folder/'timeline.jsonl'
        rp.write_text(json.dumps(result))
        tp.write_text('\n'.join(json.dumps(row) for row in rows)+'\n')
        return rp, tp

    def test_large_clock_offset_is_corrected_and_private_note_excluded(self):
        with tempfile.TemporaryDirectory() as directory:
            result = summarize(*self.fixture(pathlib.Path(directory)))
        self.assertEqual(result['sql_read_helper_calls'],10)
        self.assertEqual(result['allocated_bytes'],1000)
        self.assertNotIn('server_source_note',result['source'])

    def test_unbounded_clock_drift_or_uncertainty_is_rejected(self):
        for drift, uncertainty in [(101_000_000,1_000_000),(0,101_000_000)]:
            with self.subTest(drift=drift), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(ValueError):
                    summarize(*self.fixture(pathlib.Path(directory),drift,uncertainty))

    def test_checkpoint_metadata_uses_effective_default(self):
        self.assertEqual(checkpoint_metadata(100,0)['wal_threshold_bytes'],4 << 20)
        self.assertTrue(checkpoint_metadata(100,0)['threshold_defaulted'])
        self.assertEqual(checkpoint_metadata(100,1 << 20)['wal_threshold_bytes'],1 << 20)
        self.assertIsNone(checkpoint_metadata(0,4 << 20)['wal_threshold_bytes'])


if __name__ == '__main__':
    unittest.main()
