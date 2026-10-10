#!/usr/bin/env python3
"""Run as a web init container while its single Recreate replica is stopped."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import sqlite3
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


class MigrationError(Exception):
    pass


def save(path, value):
    temporary = path.with_suffix('.pending')
    with open(temporary, 'w', opener=lambda name, flags: os.open(name, flags, 0o600)) as stream:
        json.dump(value, stream, sort_keys=True)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)
    directory = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def request(pool, path, body=None):
    token = Path(pool['tokenFile']).read_text().strip()
    data = None if body is None else json.dumps(body).encode()
    outgoing = urllib.request.Request(pool['url'].rstrip('/') + path, data=data,
        headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(outgoing, timeout=30) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        # Never emit endpoint bodies, URLs, credentials or callback state.
        return error.code, None
    except (urllib.error.URLError, TimeoutError) as error:
        raise MigrationError('controller transport unavailable') from error


def state(pool, workspace, profile):
    path = '/v1/workspaces/' + urllib.parse.quote(workspace, safe='')
    code, value = request(pool, path + '?profile=' + urllib.parse.quote(profile, safe=''))
    return code, None if value is None else value['state']


def released(s):
    return (s['phase'] == 'released' and not s.get('handle') and not s.get('identity')
        and not s.get('endpoint') and s.get('completedOperation') == s['request']['operationId'])


def migrate_controller(pool, entry, persist, source, target, timeout):
    workspace = entry['before']['workspace_id']
    code, current = state(pool, workspace, source)
    if code != 200:
        target_code, current_target = state(pool, workspace, target)
        if target_code == 200 and entry.get('migration') and released(current_target):
            migration = entry['migration']
            if (current_target['request']['revision'] != migration['expectedRevision']
                    or current_target['epoch'] != migration['expectedEpoch']):
                raise MigrationError('target controller revision changed')
            return current_target['request']['revision']
        if code == 404 and target_code == 404 and not entry.get('release') and not entry.get('migration'):
            return entry['before']['admission_revision']
        raise MigrationError('workspace controller state cannot be reconciled')
    if current['request']['workspaceId'] != workspace:
        raise MigrationError('controller returned another workspace')
    if not released(current):
        if 'release' not in entry:
            if not current.get('identity') or not current.get('handle'):
                raise MigrationError('workspace has no stable allocation to release')
            if current.get('completedOperation') != current['request']['operationId']:
                raise MigrationError('workspace has an unfinished lifecycle operation')
            revision = max(entry['before']['admission_revision'], current['request']['revision']) + 1
            entry['release'] = {
                'workspaceId': workspace, 'profile': source, 'action': 'release_compute',
                'operationId': 'profile-migration:' + str(uuid.uuid4()), 'revision': revision,
                'expectedAllocationId': current['handle']['allocationId'],
                'expectedGeneration': current['epoch'],
                'expectedIncarnation': current['identity']['incarnation'],
            }
            persist()
        release = entry['release']
        code, submission = request(pool, '/v1/operations', release)
        if code != 202:
            raise MigrationError('release admission rejected (HTTP %s)' % code)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            code, current = state(pool, workspace, source)
            if code == 200 and released(current):
                if current['request']['operationId'] != release['operationId']:
                    raise MigrationError('workspace release superseded')
                break
            status_code, task = request(pool, '/v1/operations/' + urllib.parse.quote(submission['taskId'], safe=''))
            if status_code != 200 or task['status'] in ('FAILED', 'CANCELLED'):
                raise MigrationError('workspace release did not complete')
            time.sleep(1)
        else:
            raise MigrationError('workspace release timed out; retry the same ledger')
    if entry.get('release') and current['request']['operationId'] != entry['release']['operationId']:
        raise MigrationError('workspace release superseded')
    migration = {
        'workspaceId': workspace, 'fromProfile': source, 'toProfile': target,
        'expectedProfileHash': current['profileHash'],
        'expectedRevision': current['request']['revision'], 'expectedEpoch': current['epoch'],
    }
    if entry.get('migration') and entry['migration'] != migration:
        raise MigrationError('controller changed after migration was prepared')
    entry['migration'] = migration
    persist()
    code, migrated = request(pool, '/v1/profile-migrations', entry['migration'])
    if code != 200 or not released(migrated) or migrated['request']['profile'] != target:
        raise MigrationError('profile migration rejected (HTTP %s)' % code)
    return migrated['request']['revision']


def apply(db_path, pools_path, ledger_path, source, target, timeout=300):
    if source == target:
        raise MigrationError('source and target profiles must differ')
    pools_config = json.loads(pools_path.read_text())
    pools = {pool['id']: pool for pool in pools_config['pools']}
    if any(pool['profile'] != target for pool in pools.values()):
        raise MigrationError('all pool defaults must name the target profile')
    scope = {'database': str(db_path.resolve()), 'source': source, 'target': target,
             'pools': {key: value['url'] for key, value in pools.items()}}
    ledger_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with ledger_path.with_suffix('.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if not db_path.exists():
            if ledger_path.exists() or ledger_path.with_suffix('.sqlite-backup').exists():
                raise MigrationError('retained migration state exists but its database is missing')
            print(json.dumps({'status': 'fresh-install', 'profile': target}), flush=True)
            return
        db = sqlite3.connect('file:' + urllib.parse.quote(str(db_path.resolve()), safe='/') + '?mode=rw', uri=True, timeout=5)
        db.row_factory = sqlite3.Row
        try:
            if ledger_path.exists():
                ledger = json.loads(ledger_path.read_text())
                if ledger['scope'] != scope:
                    raise MigrationError('ledger belongs to a different migration')
            else:
                backup = ledger_path.with_suffix('.sqlite-backup')
                # No mutations begin before the ledger exists. An interrupted
                # initial backup can therefore be regenerated on the next init.
                temporary_backup = backup.with_suffix('.sqlite-backup.pending')
                temporary_backup.unlink(missing_ok=True)
                fd = os.open(temporary_backup, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
                os.close(fd)
                destination = sqlite3.connect(temporary_backup)
                try:
                    db.backup(destination)
                finally:
                    destination.close()
                with temporary_backup.open('rb') as stream:
                    os.fsync(stream.fileno())
                os.replace(temporary_backup, backup)
                directory = os.open(backup.parent, os.O_RDONLY)
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
                ledger = {'scope': scope, 'backup': str(backup), 'workspaces': {}}
                save(ledger_path, ledger)
            if ledger.get('complete'):
                if db.execute('SELECT 1 FROM agent_runtime_workspaces WHERE profile_id=? LIMIT 1', (source,)).fetchone():
                    raise MigrationError('completed migration has source-profile bindings')
                return
            persist = lambda: save(ledger_path, ledger)
            rows = db.execute('SELECT workspace_id,pool_id,profile_id,admission_revision,lifecycle_request_json FROM agent_runtime_workspaces').fetchall()
            for row in rows:
                workspace = row['workspace_id']
                if row['profile_id'] not in (None, source, target):
                    raise MigrationError('workspace has an unexpected execution profile')
                if row['profile_id'] == target and workspace not in ledger['workspaces']:
                    continue
                entry = ledger['workspaces'].setdefault(workspace, {'before': dict(row)})
                persist()
                before = entry['before']
                pool_id = before['pool_id'] or pools_config['defaultPoolId']
                if pool_id not in pools:
                    raise MigrationError('workspace pool is not configured')
                db.execute('BEGIN EXCLUSIVE')
                try:
                    actual = dict(db.execute('SELECT workspace_id,pool_id,profile_id,admission_revision,lifecycle_request_json FROM agent_runtime_workspaces WHERE workspace_id=?', (workspace,)).fetchone())
                    if entry.get('done'):
                        if actual != entry['after']:
                            raise MigrationError('completed workspace changed during maintenance')
                        db.rollback()
                        continue
                    if actual != before:
                        # Commit may have succeeded just before the process stopped.
                        if actual != entry.get('after'):
                            raise MigrationError('workspace database compare-and-swap failed')
                        revision = migrate_controller(pools[pool_id], entry, persist, source, target, timeout)
                        if actual['admission_revision'] < revision:
                            raise MigrationError('database is behind migrated controller')
                        db.rollback()
                    else:
                        revision = migrate_controller(pools[pool_id], entry, persist, source, target, timeout)
                        after = {**before, 'pool_id': pool_id, 'profile_id': target,
                            'admission_revision': max(before['admission_revision'], revision), 'lifecycle_request_json': None}
                        entry['after'] = after
                        persist()
                        changed = db.execute('UPDATE agent_runtime_workspaces SET pool_id=?,profile_id=?,admission_revision=?,lifecycle_request_json=NULL WHERE workspace_id=? AND pool_id IS ? AND profile_id IS ? AND admission_revision=? AND lifecycle_request_json IS ?',
                            (pool_id,target,after['admission_revision'],workspace,before['pool_id'],before['profile_id'],before['admission_revision'],before['lifecycle_request_json']))
                        if changed.rowcount != 1:
                            raise MigrationError('workspace database compare-and-swap failed')
                        db.commit()
                    entry['done'] = True
                    persist()
                    print(json.dumps({'workspaceId': workspace, 'profile': target, 'status': 'migrated'}), flush=True)
                except BaseException:
                    db.rollback()
                    raise
            ledger['complete'] = True
            persist()
        finally:
            db.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--database', type=Path, required=True)
    parser.add_argument('--pools', type=Path, required=True)
    parser.add_argument('--ledger', type=Path, required=True)
    parser.add_argument('--from-profile', required=True)
    parser.add_argument('--to-profile', required=True)
    parser.add_argument('--apply', action='store_true', required=True)
    args = parser.parse_args()
    try:
        apply(args.database,args.pools,args.ledger,args.from_profile,args.to_profile)
    except Exception as error:
        detail = str(error) if isinstance(error, MigrationError) else type(error).__name__
        parser.exit(1, 'Profile migration stopped: ' + detail + '\n')


if __name__ == '__main__':
    main()
