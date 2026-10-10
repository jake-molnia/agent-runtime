import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('migration', Path(__file__).with_name('migrate-execution-profiles.py'))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class MigrationTests(unittest.TestCase):
    def test_release_migrate_and_resume_after_controller_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database, pools, ledger = root/'state.sqlite', root/'pools.json', root/'ledger.json'
            db = sqlite3.connect(database)
            db.execute('CREATE TABLE agent_runtime_workspaces(workspace_id TEXT PRIMARY KEY,pool_id TEXT,profile_id TEXT,admission_revision INTEGER,lifecycle_request_json TEXT)')
            db.execute('INSERT INTO agent_runtime_workspaces VALUES (?,?,?,?,?)', ('workspace','pool','v1',7,'{"profile":"v1"}'))
            db.commit()
            pools.write_text(json.dumps({'defaultPoolId':'pool','pools':[{'id':'pool','profile':'v2','url':'http://controller','tokenFile':'unused'}]}))
            current = {'phase':'suspended','request':{'workspaceId':'workspace','revision':10,'operationId':'old','profile':'v1'},'completedOperation':'old','epoch':4,'identity':{'incarnation':'worker'},'handle':{'allocationId':'4'},'profileHash':'original'}
            calls = []
            fail_once = [True]
            def request(pool, path, body=None):
                if path.startswith('/v1/workspaces/'):
                    if ('profile='+current['request']['profile']) not in path:
                        return 503, None
                    return 200, {'state':json.loads(json.dumps(current))}
                calls.append(path)
                if path == '/v1/operations':
                    self.assertEqual(body['revision'], 11)
                    current.update(phase='released',request=body,completedOperation=body['operationId'],identity=None,handle=None)
                    return 202, {'taskId':'task'}
                if path == '/v1/profile-migrations':
                    self.assertEqual(body['expectedRevision'], 11)
                    current['request']['profile']='v2'
                    current['profileHash']='new'
                    if fail_once[0]:
                        fail_once[0]=False
                        raise migration.MigrationError('simulated response loss after commit')
                    return 200, current
                self.fail(path)
            with patch.object(migration, 'request', request):
                with self.assertRaises(migration.MigrationError):
                    migration.apply(database,pools,ledger,'v1','v2')
                self.assertEqual(db.execute('SELECT profile_id FROM agent_runtime_workspaces').fetchone()[0], 'v1')
                original_save = migration.save
                def fail_after_database_commit(path, value):
                    if any(entry.get('done') for entry in value.get('workspaces', {}).values()):
                        raise migration.MigrationError('simulated stop after database commit')
                    original_save(path, value)
                with patch.object(migration, 'save', fail_after_database_commit):
                    with self.assertRaises(migration.MigrationError):
                        migration.apply(database,pools,ledger,'v1','v2')
                self.assertEqual(db.execute('SELECT profile_id,admission_revision,lifecycle_request_json FROM agent_runtime_workspaces').fetchone(), ('v2',11,None))
                migration.apply(database,pools,ledger,'v1','v2')
                self.assertEqual(calls.count('/v1/operations'),1)
                backup=sqlite3.connect(ledger.with_suffix('.sqlite-backup'))
                self.assertEqual(backup.execute('SELECT profile_id FROM agent_runtime_workspaces').fetchone()[0],'v1')
                backup.close()
                # A normal later web run may advance revisions. Its next init is a no-op.
                db.execute('UPDATE agent_runtime_workspaces SET admission_revision=20')
                db.commit()
                migration.apply(database,pools,ledger,'v1','v2')
            db.close()

    def test_no_controller_allocation_preserves_higher_database_revision(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            db=sqlite3.connect(root/'db')
            db.execute('CREATE TABLE agent_runtime_workspaces(workspace_id TEXT,pool_id TEXT,profile_id TEXT,admission_revision INTEGER,lifecycle_request_json TEXT)')
            db.execute('INSERT INTO agent_runtime_workspaces VALUES (?,?,?,?,?)',('new',None,None,23,'old'))
            db.commit()
            (root/'pools').write_text(json.dumps({'defaultPoolId':'p','pools':[{'id':'p','profile':'v2','url':'http://x','tokenFile':'unused'}]}))
            with patch.object(migration,'request',return_value=(404,None)):
                migration.apply(root/'db',root/'pools',root/'ledger','v1','v2')
            self.assertEqual(db.execute('SELECT pool_id,profile_id,admission_revision,lifecycle_request_json FROM agent_runtime_workspaces').fetchone(),('p','v2',23,None))
            db.close()


if __name__ == '__main__':
    unittest.main()
